-- e-way bills remember the aadesh document type so lists can link to the right page.
ALTER TABLE eway_bills ADD COLUMN IF NOT EXISTS doc_type TEXT NOT NULL DEFAULT '';
