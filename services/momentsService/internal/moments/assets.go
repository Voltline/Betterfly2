package moments

import (
	"Betterfly2/shared/db"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Media interface {
	Upload(context.Context, string, string, int64) (string, map[string]string, error)
	Read(context.Context, string) (io.ReadCloser, error)
	Seal(context.Context, string, []byte, string) error
	Download(context.Context, string) (string, error)
	Avatar(context.Context, string) (string, error)
	Health(context.Context) error
}

func randomKey() string {
	var data [24]byte
	if _, err := rand.Read(data[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(data[:])
}
func assetView(row db.MomentAsset) Asset { return Asset{id(row.ID), row.Width, row.Height} }
func (s *Service) PrepareAsset(ctx context.Context, viewer int64, input CreateAsset) (*Upload, error) {
	if err := validateAsset(input); err != nil {
		return nil, err
	}
	row := db.MomentAsset{OwnerUserID: viewer, ClientAssetID: input.ClientAssetID, Hash: input.Hash, Size: input.Size, MIME: input.MIME, StagingKey: "staging/" + randomKey(), StorageKey: "sealed/" + randomKey(), Status: "pending", CreatedAt: now()}
	q := s.db.WithContext(ctx)
	result := q.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "owner_user_id"}, {Name: "client_asset_id"}}, DoNothing: true}).Create(&row)
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected == 0 {
		if err := q.Where("owner_user_id = ? AND client_asset_id = ?", viewer, input.ClientAssetID).Take(&row).Error; err != nil {
			return nil, err
		}
		if row.Hash != input.Hash || row.Size != input.Size || row.MIME != input.MIME {
			return nil, ErrConflict
		}
	}
	response := &Upload{Asset: assetView(row), Status: row.Status}
	if row.Status == "verified" {
		return response, nil
	}
	url, headers, err := s.media.Upload(ctx, row.StagingKey, row.MIME, row.Size)
	if err != nil {
		return nil, err
	}
	expires := now().Add(10 * time.Minute)
	response.UploadURL, response.Headers, response.ExpiresAt = url, headers, &expires
	return response, nil
}
func verifyImage(reader io.Reader, row db.MomentAsset) ([]byte, int, int, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaxImageBytes+1))
	if err != nil {
		return nil, 0, 0, err
	}
	if int64(len(data)) != row.Size || int64(len(data)) > MaxImageBytes {
		return nil, 0, 0, ErrInvalid
	}
	hash := sha512.Sum512(data)
	if hex.EncodeToString(hash[:]) != row.Hash {
		return nil, 0, 0, ErrInvalid
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || "image/"+format != row.MIME || config.Width <= 0 || config.Height <= 0 || config.Width > 8192 || config.Height > 8192 || int64(config.Width)*int64(config.Height) > 40_000_000 {
		return nil, 0, 0, ErrInvalid
	}
	// Decode the bounded image too: a valid header alone must not verify a truncated file.
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return nil, 0, 0, ErrInvalid
	}
	return data, config.Width, config.Height, nil
}
func (s *Service) VerifyAsset(ctx context.Context, viewer, assetID int64) (*Upload, error) {
	var row db.MomentAsset
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND owner_user_id = ?", assetID, viewer).Take(&row).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return err
		}
		if row.Status == "verified" {
			return nil
		}
		reader, err := s.media.Read(ctx, row.StagingKey)
		if err != nil {
			return err
		}
		defer reader.Close()
		data, width, height, err := verifyImage(reader, row)
		if err != nil {
			return err
		}
		// Seal these exact verified bytes, never copy a mutable staging object later.
		if err := s.media.Seal(ctx, row.StorageKey, data, row.MIME); err != nil {
			return err
		}
		if err := tx.Model(&row).Updates(map[string]any{"status": "verified", "width": width, "height": height}).Error; err != nil {
			return err
		}
		row.Status, row.Width, row.Height = "verified", width, height
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &Upload{Asset: assetView(row), Status: row.Status}, nil
}
func (s *Service) AssetDownload(ctx context.Context, viewer, assetID int64) (*Download, error) {
	q := s.db.WithContext(ctx)
	var row db.MomentAsset
	if err := q.Where("id = ? AND status = 'verified'", assetID).Take(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	if row.PostID == nil {
		if row.OwnerUserID != viewer {
			return nil, ErrNotFound
		}
	} else {
		if _, err := postFor(q, viewer, *row.PostID, false); err != nil {
			return nil, err
		}
	}
	url, err := s.media.Download(ctx, row.StorageKey)
	if err != nil {
		return nil, fmt.Errorf("download signing: %w", err)
	}
	return &Download{URL: url, ExpiresAt: now().Add(time.Minute)}, nil
}

func (s *Service) Avatar(ctx context.Context, userID int64) (*Download, error) {
	q := s.db.WithContext(ctx)
	var user db.User
	if err := q.Select("avatar").Where("id = ?", userID).Take(&user).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	// Resolve only verified legacy avatar metadata; never fetch arbitrary user URLs.
	if len(user.Avatar) != 128 {
		return nil, ErrNotFound
	}
	var file db.FileMetadata
	if err := q.Where("file_hash = ? AND is_verified = true", user.Avatar).Take(&file).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	url, err := s.media.Avatar(ctx, file.StoragePath)
	if err != nil {
		return nil, err
	}
	return &Download{URL: url, ExpiresAt: now().Add(time.Minute)}, nil
}
