package platform

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"syscall"
	"time"

	"Betterfly2/shared/logger"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

const (
	webhookBatch      = 10
	webhookMaxBackoff = 5 * time.Minute
	signatureHeader   = "X-Betterfly-Bot-Signature"
	timestampHeader   = "X-Betterfly-Bot-Timestamp"
)

var webhookDeliveries = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "betterfly_chatbot_webhook_deliveries_total",
	Help: "webhook投递结果",
}, []string{"result"})

var errPrivateAddress = errors.New("webhook不能指向内网、回环或保留地址")

// publicIP 判断地址能否作为webhook目标，防止借机器人平台访问内网服务。
func publicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() ||
		ip.Equal(net.IPv4bcast) || (ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xC0 == 64)) // 100.64.0.0/10
}

func validateWebhookURL(ctx context.Context, raw string, allowPrivate bool) error {
	if raw == "" {
		return errors.New("url不能为空；清除webhook请用DELETE")
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Hostname() == "" || parsed.User != nil {
		return errors.New("url必须是不带用户信息的http或https地址")
	}
	if allowPrivate {
		return nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, parsed.Hostname())
	if err != nil || len(addresses) == 0 {
		return errors.New("无法解析webhook主机名")
	}
	for _, address := range addresses {
		if !publicIP(address.IP) {
			return errPrivateAddress
		}
	}
	return nil
}

// newWebhookClient 在建立连接时再检查一次目标地址，域名解析结果被篡改也不能连到内网；不跟随重定向。
func newWebhookClient(timeout time.Duration, allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: timeout}
	if !allowPrivate {
		dialer.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || !publicIP(ip) {
				return errPrivateAddress
			}
			return nil
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = dialer.DialContext
	return &http.Client{
		Timeout: timeout, Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// Sign 计算webhook签名：HMAC-SHA256(secret, timestamp + "." + body)，十六进制。
func Sign(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

// Dispatcher 把更新按顺序推送到webhook。每次领取一个机器人并持有租约，多实例部署也不会乱序或重复并发投递。
type Dispatcher struct {
	platform *Platform
	client   *http.Client
	wake     chan struct{}
}

// NewDispatcher 须在开始消费Kafka之前调用，新更新写入时才能唤醒投递。
func NewDispatcher(platform *Platform) *Dispatcher {
	dispatcher := &Dispatcher{
		platform: platform,
		client:   newWebhookClient(platform.config.WebhookTimeout, platform.config.WebhookAllowPrivate),
		wake:     make(chan struct{}, 1),
	}
	platform.onQueued = func() {
		select {
		case dispatcher.wake <- struct{}{}:
		default:
		}
	}
	return dispatcher
}

// Run 循环投递，直到ctx取消。新更新写入时会唤醒，其他实例写入的更新每秒检查一次。
func (d *Dispatcher) Run(ctx context.Context, workers int) {
	for i := 0; i < workers; i++ {
		go d.worker(ctx)
	}
	<-ctx.Done()
}

func (d *Dispatcher) worker(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		worked, err := d.deliverOne(ctx)
		if err != nil && ctx.Err() == nil {
			logger.Sugar().Warnw("webhook投递循环出错", "error", err)
		}
		if worked {
			continue
		}
		select {
		case <-ctx.Done():
		case <-ticker.C:
		case <-d.wake:
		}
	}
}

func (d *Dispatcher) lease() time.Duration {
	return time.Duration(webhookBatch)*d.platform.config.WebhookTimeout + 10*time.Second
}

func backoff(failures int) time.Duration {
	if failures > 8 {
		return webhookMaxBackoff
	}
	return min(time.Duration(1<<failures)*time.Second, webhookMaxBackoff)
}

// deliverOne 领取一个机器人，按顺序投递最多webhookBatch条更新；返回是否领取到。
func (d *Dispatcher) deliverOne(ctx context.Context) (bool, error) {
	store := d.platform.store
	bot, err := store.ClaimWebhook(ctx, d.lease())
	if err != nil || bot == nil {
		return false, err
	}
	rows, err := store.PendingUpdates(ctx, bot.UserID, 0, webhookBatch)
	if err == nil {
		var updates []Update
		if updates, err = decodeUpdates(rows); err == nil {
			for _, update := range updates {
				if err = d.post(ctx, bot, update); err != nil {
					break
				}
				if err = store.AckUpdates(ctx, bot.UserID, update.UpdateID); err != nil {
					break
				}
			}
		}
	}
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err == nil {
		return true, store.FinishWebhook(finishCtx, bot.UserID, nil, "")
	}
	retryAt := d.platform.now().Add(backoff(bot.WebhookFailures))
	logger.Sugar().Infow("webhook投递失败", "bot_user_id", bot.UserID, "failures", bot.WebhookFailures+1, "retry_at", retryAt, "error", err)
	return true, store.FinishWebhook(finishCtx, bot.UserID, &retryAt, err.Error())
}

func (d *Dispatcher) post(ctx context.Context, bot *BotInfo, update Update) error {
	body, err := json.Marshal(update)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, bot.WebhookURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Betterfly-Bot-Platform/1")
	if bot.WebhookSecret != "" {
		timestamp := strconv.FormatInt(d.platform.now().Unix(), 10)
		request.Header.Set(timestampHeader, timestamp)
		request.Header.Set(signatureHeader, Sign(bot.WebhookSecret, timestamp, body))
	}
	response, err := d.client.Do(request)
	if err != nil {
		webhookDeliveries.WithLabelValues("error").Inc()
		return fmt.Errorf("请求webhook失败: %w", err)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	_ = response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		webhookDeliveries.WithLabelValues("status").Inc()
		return fmt.Errorf("webhook返回HTTP %d", response.StatusCode)
	}
	webhookDeliveries.WithLabelValues("ok").Inc()
	return nil
}

// CleanupLoop 定期删除超过保留期的更新。
func (p *Platform) CleanupLoop(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		deleted, err := p.store.DeleteUpdatesBefore(ctx, p.now().Add(-p.config.UpdateRetention))
		if err != nil {
			logger.Sugar().Warnw("清理机器人更新失败", "error", err)
		} else if deleted > 0 {
			logger.Sugar().Infow("清理过期机器人更新", "deleted", deleted)
		}
	}
}
