import { describe, expect, it } from 'vitest'
import { localizedAnnouncementContent, localizedAnnouncementTitle } from './announcementLocalization'

const localized = {
  title: '中文兼容标题',
  content: '中文兼容正文',
  title_zh: '中文标题',
  title_en: 'English title',
  content_zh: '中文正文',
  content_en: 'English body',
}

describe('announcement localization', () => {
  it('uses the current UI language when that translation is available', () => {
    expect(localizedAnnouncementTitle(localized, 'zh')).toBe('中文标题')
    expect(localizedAnnouncementContent(localized, 'en-US')).toBe('English body')
  })

  it('falls back to the opposite translation, then the legacy field', () => {
    expect(localizedAnnouncementTitle({ ...localized, title_en: '' }, 'en')).toBe('中文标题')
    expect(localizedAnnouncementContent({ content: 'Legacy content' }, 'en')).toBe('Legacy content')
  })
})
