-- kar: GSTIN cache, HSN/SAC master, tenant tax settings, e-Way bill & e-Invoice payloads

CREATE TABLE IF NOT EXISTS processed_events (
    event_id     TEXT PRIMARY KEY,
    event_type   TEXT NOT NULL,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Public GST registry data, shared by all tenants on purpose: a GSTIN's legal
-- name/status is the same for everyone and every live GSP lookup costs money.
-- It holds no tenant data, so it has no tenant_id.
CREATE TABLE gstin_cache (
    gstin      TEXT PRIMARY KEY,
    result     JSONB NOT NULL,
    source     TEXT NOT NULL,
    fetched_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- HSN/SAC reference master (global reference data, like gstin_cache).
CREATE TABLE hsn_codes (
    code            TEXT PRIMARY KEY,
    description     TEXT NOT NULL,
    kind            TEXT NOT NULL CHECK (kind IN ('goods','service')),
    default_rate_bp INT NOT NULL,
    cess_rate_bp    INT NOT NULL DEFAULT 0
);
CREATE INDEX hsn_codes_desc ON hsn_codes USING gin (to_tsvector('simple', description));

CREATE TABLE tax_settings (
    tenant_id            UUID PRIMARY KEY,
    state_code           TEXT NOT NULL,
    gstin                TEXT,
    composition          BOOLEAN NOT NULL DEFAULT false,
    einvoice_enabled     BOOLEAN NOT NULL DEFAULT false,      -- turnover above the e-invoicing limit
    eway_threshold_paise BIGINT NOT NULL DEFAULT 5000000,     -- ₹50,000
    b2cl_threshold_paise BIGINT NOT NULL DEFAULT 10000000,    -- ₹1,00,000 (GSTR-1 B2CL limit)
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE eway_bills (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id       UUID NOT NULL,
    invoice_id      UUID NOT NULL,
    invoice_number  TEXT NOT NULL,
    payload         JSONB NOT NULL,
    below_threshold BOOLEAN NOT NULL,
    warnings        JSONB NOT NULL DEFAULT '[]',
    ewb_no          TEXT,               -- filled once the bill is generated on the NIC portal
    created_by      TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, invoice_id)
);

CREATE TABLE einvoices (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id      UUID NOT NULL,
    invoice_id     UUID NOT NULL,
    invoice_number TEXT NOT NULL,
    payload        JSONB NOT NULL,
    irn            TEXT,                -- filled once registered on the IRP
    created_by     TEXT NOT NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, invoice_id)
);

-- Starter HSN/SAC set for Indian retail. Rates follow the GST 2.0 slabs
-- (5% / 18% / 40%, effective 22-Sep-2025). They are DEFAULTS for the item
-- master only — verify against the latest CBIC notification before relying on them.
INSERT INTO hsn_codes (code, description, kind, default_rate_bp) VALUES
 ('0401','Milk and cream, not concentrated (fresh, UHT)','goods',0),
 ('0402','Milk powder, condensed milk','goods',500),
 ('0403','Curd, yoghurt, buttermilk','goods',500),
 ('0405','Butter, ghee, dairy spreads','goods',500),
 ('0406','Cheese and paneer','goods',500),
 ('0713','Dried pulses (dal), unbranded','goods',0),
 ('0901','Coffee, roasted or not','goods',500),
 ('0902','Tea','goods',500),
 ('0904','Pepper, chilli (spices)','goods',500),
 ('0910','Turmeric, ginger and other spices','goods',500),
 ('1001','Wheat','goods',0),
 ('1006','Rice','goods',500),
 ('1101','Wheat flour (atta, maida)','goods',500),
 ('1507','Soyabean oil','goods',500),
 ('1508','Groundnut oil','goods',500),
 ('1512','Sunflower oil','goods',500),
 ('1515','Mustard and other vegetable oils','goods',500),
 ('1701','Cane or beet sugar','goods',500),
 ('1704','Sugar confectionery','goods',500),
 ('1806','Chocolate and cocoa preparations','goods',500),
 ('1902','Pasta, noodles, vermicelli','goods',500),
 ('1904','Cornflakes, puffed cereals','goods',500),
 ('1905','Biscuits, cakes, bakery products','goods',500),
 ('2101','Instant coffee, tea extracts','goods',500),
 ('2106','Namkeen, bhujia, food preparations n.e.s.','goods',500),
 ('2201','Packaged drinking water','goods',500),
 ('2202','Aerated / sweetened beverages','goods',4000),
 ('2402','Cigarettes','goods',4000),
 ('2501','Salt','goods',0),
 ('2523','Cement','goods',1800),
 ('3004','Medicaments in measured doses','goods',500),
 ('3304','Beauty and make-up preparations','goods',1800),
 ('3305','Shampoo, hair oil','goods',500),
 ('3306','Toothpaste, dental preparations','goods',500),
 ('3307','Shaving preparations, deodorants','goods',1800),
 ('3401','Soap','goods',500),
 ('3402','Detergents, washing preparations','goods',1800),
 ('3808','Insecticides, mosquito repellents','goods',1800),
 ('3923','Plastic boxes, bags, containers','goods',1800),
 ('4820','Notebooks, registers, exercise books','goods',0),
 ('4901','Printed books','goods',0),
 ('6109','T-shirts, vests (knitted)','goods',500),
 ('6203','Men''s suits, trousers, shirts','goods',500),
 ('6204','Women''s suits, dresses, skirts','goods',500),
 ('6403','Footwear with leather uppers','goods',500),
 ('6911','Ceramic tableware and kitchenware','goods',1800),
 ('7323','Steel kitchen utensils','goods',500),
 ('8414','Fans, air pumps','goods',1800),
 ('8415','Air conditioners','goods',1800),
 ('8418','Refrigerators, freezers','goods',1800),
 ('8450','Washing machines','goods',1800),
 ('8471','Computers, laptops','goods',1800),
 ('8504','Chargers, adaptors, inverters','goods',1800),
 ('8507','Batteries (accumulators)','goods',1800),
 ('8516','Water heaters, irons, hair dryers','goods',1800),
 ('8517','Mobile phones','goods',1800),
 ('8528','Televisions, monitors','goods',1800),
 ('8539','LED lamps and bulbs','goods',1800),
 ('9403','Furniture','goods',1800),
 ('9503','Toys','goods',500),
 ('9603','Brooms, brushes, toothbrushes','goods',500),
 ('9619','Sanitary napkins, diapers','goods',0),
 ('9954','Construction services','service',1800),
 ('9963','Restaurant and accommodation services','service',500),
 ('9965','Goods transport services','service',500),
 ('9971','Financial and related services','service',1800),
 ('9983','Professional, technical and IT services','service',1800),
 ('9987','Repair and maintenance services','service',1800),
 ('9997','Other services (laundry, beauty, etc.)','service',1800)
ON CONFLICT (code) DO NOTHING;
