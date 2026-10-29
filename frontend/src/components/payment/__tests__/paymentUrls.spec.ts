import { describe, expect, it, vi } from 'vitest'
import { buildPaymentResultUrl, resolvePaymentUrl } from '../paymentUrls'
import type { Router } from 'vue-router'

describe('payment URL helpers', () => {
  it('keeps the router base when building an absolute Stripe return URL', () => {
    const router = {
      resolve: vi.fn(() => ({ href: '/console/payment/result?order_id=42&status=success' })),
    } as unknown as Router

    expect(buildPaymentResultUrl(router, 42)).toBe(
      `${window.location.origin}/console/payment/result?order_id=42&status=success`,
    )
    expect(router.resolve).toHaveBeenCalledWith({
      name: 'PaymentResult',
      query: { order_id: '42', status: 'success' },
    })
  })

  it('resolves relative application routes against the current origin', () => {
    const router = {
      resolve: vi.fn(() => ({ href: '/payment/stripe?order_id=42' })),
    } as unknown as Router

    expect(resolvePaymentUrl(router, { path: '/payment/stripe', query: { order_id: '42' } })).toBe(
      `${window.location.origin}/payment/stripe?order_id=42`,
    )
  })
})
