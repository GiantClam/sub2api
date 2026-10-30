import { describe, expect, it } from 'vitest'
import { currencySymbol, formatLedgerAmount, formatPaymentAmount } from '../currency'

describe('formatPaymentAmount', () => {
  it('uses the currency default fraction digits', () => {
    expect(formatPaymentAmount(100, 'JPY', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'KRW', 'en-US')).not.toContain('.00')
    expect(formatPaymentAmount(100, 'HKD', 'en-US')).toContain('.00')
  })
})

describe('currencySymbol', () => {
  it('maps common payment currencies and falls back safely', () => {
    expect(currencySymbol('USD')).toBe('$')
    expect(currencySymbol('cny')).toBe('¥')
    expect(currencySymbol('EUR')).toBe('€')
    expect(currencySymbol('')).toBe('¥')
    expect(currencySymbol('XYZ')).toBe('XYZ')
  })
})

describe('formatLedgerAmount', () => {
  it('keeps fractional USD wallet credits visible', () => {
    expect(formatLedgerAmount(1.53846154, 'en-US')).toBe('$1.53846154')
    expect(formatLedgerAmount(10, 'en-US')).toBe('$10.00')
  })
})
