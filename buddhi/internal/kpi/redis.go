package kpi

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// ---------- Redis mirror ----------
//
// Postgres is the source of truth. Every KPI change bumps a version per
// (tenant, store, day) and (tenant, all, day) in kpi_versions inside the same
// transaction. The Redis hash for that key carries the version it reflects in
// field "seq":
//
//   - mirror applies a delta only when Redis is exactly one version behind;
//     otherwise it deletes the keys (a mirror was missed or reordered).
//   - the dashboard uses Redis only when its "seq" equals Postgres'; on a
//     mismatch it reads Postgres and rebuilds the keys from one snapshot.
//
// So a failed post-commit mirror can make the dashboard slower, never stale.

func kpiKey(tenant, store, day string) string {
	return "buddhi:kpi:" + tenant + ":" + store + ":" + day
}
func topKey(tenant, store, day string) string {
	return "buddhi:top:" + tenant + ":" + store + ":" + day
}
func payKey(tenant, store, day string) string {
	return "buddhi:pay:" + tenant + ":" + store + ":" + day
}
func nameKey(tenant string) string { return "buddhi:itemnames:" + tenant }

// applyScript: KEYS kpi, top, pay, names; ARGV prev seq, new seq, ttl seconds, payload.
// Values in the payload are strings so Lua never turns large paise into floats.
var applyScript = redis.NewScript(`
local cur = redis.call('HGET', KEYS[1], 'seq')
if not cur then
  cur = '0'
  redis.call('DEL', KEYS[2], KEYS[3])
end
if cur ~= ARGV[1] then
  redis.call('DEL', KEYS[1], KEYS[2], KEYS[3])
  return 0
end
local d = cjson.decode(ARGV[4])
for k, v in pairs(d.kpi) do redis.call('HINCRBY', KEYS[1], k, v) end
for k, v in pairs(d.items) do redis.call('ZINCRBY', KEYS[2], v, k) end
for k, v in pairs(d.names) do redis.call('HSET', KEYS[4], k, v) end
for k, v in pairs(d.pay) do redis.call('HINCRBY', KEYS[3], k, v) end
redis.call('HSET', KEYS[1], 'seq', ARGV[2])
local ttl = tonumber(ARGV[3])
for i = 1, 3 do
  if redis.call('EXISTS', KEYS[i]) == 1 then redis.call('EXPIRE', KEYS[i], ttl) end
end
return 1
`)

// rebuildScript: KEYS kpi, top, pay, names; ARGV seq, ttl seconds, payload.
var rebuildScript = redis.NewScript(`
redis.call('DEL', KEYS[1], KEYS[2], KEYS[3])
local d = cjson.decode(ARGV[3])
for k, v in pairs(d.kpi) do redis.call('HSET', KEYS[1], k, v) end
for k, v in pairs(d.items) do redis.call('ZADD', KEYS[2], v, k) end
for k, v in pairs(d.names) do redis.call('HSET', KEYS[4], k, v) end
for k, v in pairs(d.pay) do redis.call('HSET', KEYS[3], k, v) end
redis.call('HSET', KEYS[1], 'seq', ARGV[1])
local ttl = tonumber(ARGV[2])
for i = 1, 3 do
  if redis.call('EXISTS', KEYS[i]) == 1 then redis.call('EXPIRE', KEYS[i], ttl) end
end
return 1
`)

type mirrorPayload struct {
	KPI   map[string]string `json:"kpi"`
	Items map[string]string `json:"items"`
	Names map[string]string `json:"names"`
	Pay   map[string]string `json:"pay"`
}

func i64(v int64) string { return strconv.FormatInt(v, 10) }

func deltaPayload(d *delta) mirrorPayload {
	p := mirrorPayload{KPI: map[string]string{}, Items: map[string]string{}, Names: map[string]string{}, Pay: map[string]string{}}
	if d.sales != 0 || d.bills != 0 || d.cancelled != 0 || d.returns != 0 || d.tax != 0 {
		p.KPI = map[string]string{
			"sales_paise": i64(d.sales), "taxable_paise": i64(d.taxable), "tax_paise": i64(d.tax),
			"bills": i64(d.bills), "cancelled": i64(d.cancelled), "margin_paise": i64(d.margin),
			"margin_base_paise": i64(d.marginBase), "returns": i64(d.returns), "returns_paise": i64(d.returnsPaise),
		}
	}
	for key, it := range d.items {
		p.Items[key] = i64(it.sales)
		p.Names[key] = it.name
	}
	for mode, amt := range d.payments {
		p.Pay[mode] = i64(amt)
	}
	return p
}

// mirror applies d to Redis after the Postgres commit. Failures only log: the
// version check makes the dashboard fall back to Postgres and repair.
func (s *Service) mirror(ctx context.Context, tenant string, d *delta) {
	if s.Redis == nil || d == nil || d.seqStore == 0 {
		return
	}
	payload, _ := json.Marshal(deltaPayload(d))
	ttl := strconv.Itoa(int(redisTTL / time.Second))
	for _, t := range []struct {
		store string
		seq   int64
	}{{d.store, d.seqStore}, {AllStores, d.seqAll}} {
		keys := []string{kpiKey(tenant, t.store, d.day), topKey(tenant, t.store, d.day), payKey(tenant, t.store, d.day), nameKey(tenant)}
		if err := applyScript.Run(ctx, s.Redis, keys, i64(t.seq-1), i64(t.seq), ttl, string(payload)).Err(); err != nil {
			s.Log.Warn("redis kpi mirror failed; dashboard will read Postgres until repaired", "error", err.Error())
		}
	}
}

// redisView is what Redis holds for one (tenant, store, day).
type redisView struct {
	seq  int64
	day  DayKPI
	top  []TopItem
	pay  map[string]int64
	have bool
}

// readRedis reads the hash, top items and payment mix atomically (MULTI).
func (s *Service) readRedis(ctx context.Context, tenant, store, date string) (redisView, error) {
	v := redisView{pay: map[string]int64{}}
	var kpi *redis.MapStringStringCmd
	var top *redis.ZSliceCmd
	var pay *redis.MapStringStringCmd
	_, err := s.Redis.TxPipelined(ctx, func(p redis.Pipeliner) error {
		kpi = p.HGetAll(ctx, kpiKey(tenant, store, date))
		top = p.ZRevRangeWithScores(ctx, topKey(tenant, store, date), 0, 9)
		pay = p.HGetAll(ctx, payKey(tenant, store, date))
		return nil
	})
	if err != nil && err != redis.Nil {
		return v, err
	}
	m := kpi.Val()
	seq, ok := m["seq"]
	if !ok {
		return v, nil
	}
	v.have = true
	v.seq, _ = strconv.ParseInt(seq, 10, 64)
	n := func(k string) int64 { x, _ := strconv.ParseInt(m[k], 10, 64); return x }
	v.day = DayKPI{Date: date, SalesPaise: n("sales_paise"), TaxablePaise: n("taxable_paise"), TaxPaise: n("tax_paise"),
		Bills: n("bills"), Cancelled: n("cancelled"), MarginPaise: n("margin_paise"), MarginBasePaise: n("margin_base_paise"),
		Returns: n("returns"), ReturnsPaise: n("returns_paise")}
	v.day.finish()
	zs := top.Val()
	v.top = []TopItem{}
	if len(zs) > 0 {
		keys := make([]string, len(zs))
		for i, z := range zs {
			keys[i], _ = z.Member.(string)
		}
		names, _ := s.Redis.HMGet(ctx, nameKey(tenant), keys...).Result()
		for i, z := range zs {
			if z.Score <= 0 {
				continue
			}
			var name string
			if i < len(names) {
				name, _ = names[i].(string)
			}
			v.top = append(v.top, TopItem{ItemKey: keys[i], Name: name, SalesPaise: int64(z.Score)})
		}
	}
	for k, val := range pay.Val() {
		if x, _ := strconv.ParseInt(val, 10, 64); x != 0 {
			v.pay[k] = x
		}
	}
	return v, nil
}

// rebuild rewrites the Redis keys from a Postgres snapshot.
func (s *Service) rebuild(ctx context.Context, tenant, store, date string, snap *snapshot) {
	p := mirrorPayload{
		KPI: map[string]string{
			"sales_paise": i64(snap.day.SalesPaise), "taxable_paise": i64(snap.day.TaxablePaise), "tax_paise": i64(snap.day.TaxPaise),
			"bills": i64(snap.day.Bills), "cancelled": i64(snap.day.Cancelled), "margin_paise": i64(snap.day.MarginPaise),
			"margin_base_paise": i64(snap.day.MarginBasePaise), "returns": i64(snap.day.Returns), "returns_paise": i64(snap.day.ReturnsPaise),
		},
		Items: map[string]string{}, Names: map[string]string{}, Pay: map[string]string{},
	}
	for _, it := range snap.allItems {
		p.Items[it.ItemKey] = i64(it.SalesPaise)
		p.Names[it.ItemKey] = it.Name
	}
	for k, v := range snap.pay {
		p.Pay[k] = i64(v)
	}
	payload, _ := json.Marshal(p)
	keys := []string{kpiKey(tenant, store, date), topKey(tenant, store, date), payKey(tenant, store, date), nameKey(tenant)}
	if err := rebuildScript.Run(ctx, s.Redis, keys, i64(snap.seq), strconv.Itoa(int(redisTTL/time.Second)), string(payload)).Err(); err != nil {
		s.Log.Warn("redis kpi rebuild failed", "error", err.Error())
	}
}
