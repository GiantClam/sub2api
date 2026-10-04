package dto

import (
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

type Announcement struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	TitleZh    string `json:"title_zh,omitempty"`
	TitleEn    string `json:"title_en,omitempty"`
	ContentZh  string `json:"content_zh,omitempty"`
	ContentEn  string `json:"content_en,omitempty"`
	Status     string `json:"status"`
	NotifyMode string `json:"notify_mode"`

	Targeting service.AnnouncementTargeting `json:"targeting"`

	StartsAt *time.Time `json:"starts_at,omitempty"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`

	CreatedBy *int64 `json:"created_by,omitempty"`
	UpdatedBy *int64 `json:"updated_by,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type UserAnnouncement struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	TitleZh    string `json:"title_zh,omitempty"`
	TitleEn    string `json:"title_en,omitempty"`
	ContentZh  string `json:"content_zh,omitempty"`
	ContentEn  string `json:"content_en,omitempty"`
	NotifyMode string `json:"notify_mode"`

	StartsAt *time.Time `json:"starts_at,omitempty"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`

	ReadAt *time.Time `json:"read_at,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func AnnouncementFromService(a *service.Announcement) *Announcement {
	if a == nil {
		return nil
	}
	return &Announcement{
		ID:         a.ID,
		Title:      a.Title,
		Content:    a.Content,
		TitleZh:    a.TitleZh,
		TitleEn:    a.TitleEn,
		ContentZh:  a.ContentZh,
		ContentEn:  a.ContentEn,
		Status:     a.Status,
		NotifyMode: a.NotifyMode,
		Targeting:  a.Targeting,
		StartsAt:   a.StartsAt,
		EndsAt:     a.EndsAt,
		CreatedBy:  a.CreatedBy,
		UpdatedBy:  a.UpdatedBy,
		CreatedAt:  a.CreatedAt,
		UpdatedAt:  a.UpdatedAt,
	}
}

func UserAnnouncementFromService(a *service.UserAnnouncement) *UserAnnouncement {
	if a == nil {
		return nil
	}
	return &UserAnnouncement{
		ID:         a.Announcement.ID,
		Title:      a.Announcement.Title,
		Content:    a.Announcement.Content,
		TitleZh:    a.Announcement.TitleZh,
		TitleEn:    a.Announcement.TitleEn,
		ContentZh:  a.Announcement.ContentZh,
		ContentEn:  a.Announcement.ContentEn,
		NotifyMode: a.Announcement.NotifyMode,
		StartsAt:   a.Announcement.StartsAt,
		EndsAt:     a.Announcement.EndsAt,
		ReadAt:     a.ReadAt,
		CreatedAt:  a.Announcement.CreatedAt,
		UpdatedAt:  a.Announcement.UpdatedAt,
	}
}
