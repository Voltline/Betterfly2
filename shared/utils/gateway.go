package utils

import (
	"net"
	"net/http"
	"net/url"
	"strings"
)

// GatewayOrigin 返回经网关转发的请求在客户端看来的地址（scheme://host[:port]），
// 用于让预签名URL同样走网关。网关设置X-Forwarded-Proto并原样透传Host；
// 没有该头、协议不是http(s)或Host含有主机名以外的字符时返回""。
// 结果只出现在给本次请求者的响应里，伪造的头只会影响伪造者自己。
func GatewayOrigin(r *http.Request) string {
	if r == nil {
		return ""
	}
	proto := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]))
	if proto != "http" && proto != "https" {
		return ""
	}
	host := strings.TrimSpace(r.Host)
	if host == "" || strings.Trim(host, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789.-:[]") != "" {
		return ""
	}
	name, port, err := net.SplitHostPort(host)
	if err != nil {
		name, port = strings.Trim(host, "[]"), ""
	}
	if name == "" || strings.ContainsAny(name, "[]") {
		return ""
	}
	if port != "" {
		host = net.JoinHostPort(name, port)
	} else if strings.Contains(name, ":") {
		host = "[" + name + "]"
	} else {
		host = name
	}
	return (&url.URL{Scheme: proto, Host: host}).String()
}
