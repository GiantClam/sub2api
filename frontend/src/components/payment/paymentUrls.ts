import type { RouteLocationRaw, Router } from 'vue-router'

/**
 * Resolve an application route as an absolute URL for payment providers.
 *
 * Router resolution keeps the configured history base (and any future route
 * prefix) in the URL. Stripe requires an absolute return_url, so resolve it
 * against the current origin after the router has produced the path.
 */
export function resolvePaymentUrl(router: Router, location: RouteLocationRaw): string {
  const href = router.resolve(location).href
  if (typeof window === 'undefined') {
    return href
  }
  return new URL(href, window.location.origin).toString()
}

export function buildPaymentResultUrl(router: Router, orderId: number | string): string {
  return resolvePaymentUrl(router, {
    name: 'PaymentResult',
    query: {
      order_id: String(orderId),
      status: 'success',
    },
  })
}
