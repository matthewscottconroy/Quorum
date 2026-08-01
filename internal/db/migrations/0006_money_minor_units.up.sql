-- Store money as integer minor units (e.g. cents) instead of NUMERIC major
-- units, so all arithmetic is exact integer math end-to-end. Conversion uses
-- each row's currency exponent: 0 for zero-decimal currencies (JPY, KRW, …),
-- 3 for the few three-decimal ones (BHD, KWD, …), 2 otherwise.

-- Reusable expression: minor-units multiplier for a currency code.
-- (Inlined per column below; PostgreSQL has no cheap way to share it in DDL.)

ALTER TABLE dues_invoices
    ALTER COLUMN amount TYPE BIGINT
    USING round(amount * power(10, CASE upper(currency)
        WHEN 'BIF' THEN 0 WHEN 'CLP' THEN 0 WHEN 'DJF' THEN 0 WHEN 'GNF' THEN 0
        WHEN 'JPY' THEN 0 WHEN 'KMF' THEN 0 WHEN 'KRW' THEN 0 WHEN 'MGA' THEN 0
        WHEN 'PYG' THEN 0 WHEN 'RWF' THEN 0 WHEN 'UGX' THEN 0 WHEN 'VND' THEN 0
        WHEN 'VUV' THEN 0 WHEN 'XAF' THEN 0 WHEN 'XOF' THEN 0 WHEN 'XPF' THEN 0
        WHEN 'BHD' THEN 3 WHEN 'IQD' THEN 3 WHEN 'JOD' THEN 3 WHEN 'KWD' THEN 3
        WHEN 'LYD' THEN 3 WHEN 'OMR' THEN 3 WHEN 'TND' THEN 3
        ELSE 2 END))::bigint;

ALTER TABLE transactions
    ALTER COLUMN amount TYPE BIGINT
    USING round(amount * power(10, CASE upper(currency)
        WHEN 'BIF' THEN 0 WHEN 'CLP' THEN 0 WHEN 'DJF' THEN 0 WHEN 'GNF' THEN 0
        WHEN 'JPY' THEN 0 WHEN 'KMF' THEN 0 WHEN 'KRW' THEN 0 WHEN 'MGA' THEN 0
        WHEN 'PYG' THEN 0 WHEN 'RWF' THEN 0 WHEN 'UGX' THEN 0 WHEN 'VND' THEN 0
        WHEN 'VUV' THEN 0 WHEN 'XAF' THEN 0 WHEN 'XOF' THEN 0 WHEN 'XPF' THEN 0
        WHEN 'BHD' THEN 3 WHEN 'IQD' THEN 3 WHEN 'JOD' THEN 3 WHEN 'KWD' THEN 3
        WHEN 'LYD' THEN 3 WHEN 'OMR' THEN 3 WHEN 'TND' THEN 3
        ELSE 2 END))::bigint;
