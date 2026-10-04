package dto

import (
	"encoding/json"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAnnouncementDTOIncludesTranslationsAndLegacyFields(t *testing.T) {
	announcement := &service.Announcement{
		ID: 1, Title: "Legacy title", Content: "Legacy content",
		TitleZh: "中文标题", TitleEn: "English title", ContentZh: "中文正文", ContentEn: "English content",
	}
	for name, value := range map[string]any{
		"admin": AnnouncementFromService(announcement),
		"user":  UserAnnouncementFromService(&service.UserAnnouncement{Announcement: *announcement}),
	} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(value)
			require.NoError(t, err)
			var fields map[string]any
			require.NoError(t, json.Unmarshal(encoded, &fields))
			for key, expected := range map[string]string{
				"title": "Legacy title", "content": "Legacy content",
				"title_zh": "中文标题", "title_en": "English title",
				"content_zh": "中文正文", "content_en": "English content",
			} {
				require.Equal(t, expected, fields[key], key)
			}
		})
	}
}

func TestAnnouncementDTOKeepsLegacyResponseWithoutEmptyTranslations(t *testing.T) {
	encoded, err := json.Marshal(AnnouncementFromService(&service.Announcement{
		Title: "Legacy title", Content: "Legacy content",
	}))
	require.NoError(t, err)
	var fields map[string]any
	require.NoError(t, json.Unmarshal(encoded, &fields))
	require.Equal(t, "Legacy title", fields["title"])
	require.NotContains(t, fields, "title_zh")
	require.NotContains(t, fields, "title_en")
	require.NotContains(t, fields, "content_zh")
	require.NotContains(t, fields, "content_en")
}
