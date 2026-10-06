-- USD ledger amounts retain token-billing precision. Gateway payment amounts
-- remain in their original currency and retain their existing payment precision.
ALTER TABLE payment_orders
    ALTER COLUMN amount TYPE DECIMAL(20,8),
    ALTER COLUMN bonus_amount TYPE DECIMAL(20,8),
    ALTER COLUMN refund_amount TYPE DECIMAL(20,8);
