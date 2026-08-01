-- Revert integer minor units back to NUMERIC(10,2) major units. Note: value
-- for three-decimal currencies loses its third decimal place under (10,2);
-- this is a lossy downgrade, acceptable for a rollback.

ALTER TABLE transactions
    ALTER COLUMN amount TYPE NUMERIC(10,2)
    USING (amount::numeric / power(10, CASE upper(currency)
        WHEN 'BIF' THEN 0 WHEN 'CLP' THEN 0 WHEN 'DJF' THEN 0 WHEN 'GNF' THEN 0
        WHEN 'JPY' THEN 0 WHEN 'KMF' THEN 0 WHEN 'KRW' THEN 0 WHEN 'MGA' THEN 0
        WHEN 'PYG' THEN 0 WHEN 'RWF' THEN 0 WHEN 'UGX' THEN 0 WHEN 'VND' THEN 0
        WHEN 'VUV' THEN 0 WHEN 'XAF' THEN 0 WHEN 'XOF' THEN 0 WHEN 'XPF' THEN 0
        WHEN 'BHD' THEN 3 WHEN 'IQD' THEN 3 WHEN 'JOD' THEN 3 WHEN 'KWD' THEN 3
        WHEN 'LYD' THEN 3 WHEN 'OMR' THEN 3 WHEN 'TND' THEN 3
        ELSE 2 END));

ALTER TABLE dues_invoices
    ALTER COLUMN amount TYPE NUMERIC(10,2)
    USING (amount::numeric / power(10, CASE upper(currency)
        WHEN 'BIF' THEN 0 WHEN 'CLP' THEN 0 WHEN 'DJF' THEN 0 WHEN 'GNF' THEN 0
        WHEN 'JPY' THEN 0 WHEN 'KMF' THEN 0 WHEN 'KRW' THEN 0 WHEN 'MGA' THEN 0
        WHEN 'PYG' THEN 0 WHEN 'RWF' THEN 0 WHEN 'UGX' THEN 0 WHEN 'VND' THEN 0
        WHEN 'VUV' THEN 0 WHEN 'XAF' THEN 0 WHEN 'XOF' THEN 0 WHEN 'XPF' THEN 0
        WHEN 'BHD' THEN 3 WHEN 'IQD' THEN 3 WHEN 'JOD' THEN 3 WHEN 'KWD' THEN 3
        WHEN 'LYD' THEN 3 WHEN 'OMR' THEN 3 WHEN 'TND' THEN 3
        ELSE 2 END));
