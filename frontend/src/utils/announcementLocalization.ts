type LocalizedAnnouncement = {
  title?: string
  content?: string
  title_zh?: string
  title_en?: string
  content_zh?: string
  content_en?: string
}

function text(value: unknown): string {
  return typeof value === 'string' ? value.trim() : ''
}

function isChineseLocale(locale: string): boolean {
  return locale.toLowerCase().startsWith('zh')
}

function localizedField(
  announcement: LocalizedAnnouncement | null | undefined,
  field: 'title' | 'content',
  locale: string
): string {
  const preferredLanguage = isChineseLocale(locale) ? 'zh' : 'en'
  const fallbackLanguage = preferredLanguage === 'zh' ? 'en' : 'zh'
  const preferred = text(announcement?.[`${field}_${preferredLanguage}` as keyof LocalizedAnnouncement])
  const fallback = text(announcement?.[`${field}_${fallbackLanguage}` as keyof LocalizedAnnouncement])

  return preferred || fallback || text(announcement?.[field])
}

export function localizedAnnouncementTitle(
  announcement: LocalizedAnnouncement | null | undefined,
  locale: string
): string {
  return localizedField(announcement, 'title', locale)
}

export function localizedAnnouncementContent(
  announcement: LocalizedAnnouncement | null | undefined,
  locale: string
): string {
  return localizedField(announcement, 'content', locale)
}
