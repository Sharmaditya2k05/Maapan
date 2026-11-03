package identity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// otpOutcome is the result of one OTP check.
type otpOutcome int

const (
	otpMatch   otpOutcome = iota + 1 // correct: OTP and counter deleted
	otpWrong                         // wrong: counter incremented, attempts left > 0
	otpMissing                       // expired or never requested
	otpLocked                        // locked now (this guess hit the limit) or already
)

// verifyOTPScript checks a code and counts the attempt in one atomic step, so
// parallel guesses cannot each read "4 attempts" and all get a try.
//
// KEYS: otp, attempts, lock. ARGV: candidate hash, max attempts, attempts TTL
// ms, lock TTL ms. Returns {outcome, attempts}. It uses only EXISTS, GET, DEL,
// INCR, PEXPIRE and SET PX, which Garnet's Lua support also provides.
var verifyOTPScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[3]) == 1 then return {4, 0} end
local stored = redis.call('GET', KEYS[1])
if not stored then return {3, 0} end
if stored == ARGV[1] then
  redis.call('DEL', KEYS[1], KEYS[2])
  return {1, 0}
end
local n = redis.call('INCR', KEYS[2])
if n == 1 then redis.call('PEXPIRE', KEYS[2], ARGV[3]) end
if n >= tonumber(ARGV[2]) then
  redis.call('SET', KEYS[3], '1', 'PX', ARGV[4])
  redis.call('DEL', KEYS[1], KEYS[2])
  return {4, n}
end
return {2, n}
`)

// checkOTP verifies candidateHash for phone. It runs the Lua script; if the
// server cannot run scripts (Garnet started without --lua), it falls back to
// counting the attempt with INCR *before* comparing, which is equally safe
// against parallel guessing (every guess consumes an attempt atomically).
func checkOTP(ctx context.Context, rc *redis.Client, phone, candidateHash string, maxAttempts int, attemptsTTL, lockTTL time.Duration) (otpOutcome, int, error) {
	keys := []string{otpKey(phone), attemptsKey(phone), lockKey(phone)}
	res, err := verifyOTPScript.Run(ctx, rc, keys, candidateHash, maxAttempts, attemptsTTL.Milliseconds(), lockTTL.Milliseconds()).Int64Slice()
	if err == nil && len(res) == 2 {
		return otpOutcome(res[0]), int(res[1]), nil
	}
	if err == nil {
		err = fmt.Errorf("unexpected script reply %v", res)
	}
	if ctx.Err() != nil || isConnError(err) {
		return 0, 0, err
	}
	return checkOTPNoScript(ctx, rc, keys, candidateHash, maxAttempts, attemptsTTL, lockTTL)
}

func checkOTPNoScript(ctx context.Context, rc *redis.Client, keys []string, candidateHash string, maxAttempts int, attemptsTTL, lockTTL time.Duration) (otpOutcome, int, error) {
	otpK, attK, lockK := keys[0], keys[1], keys[2]
	pipe := rc.TxPipeline()
	locked := pipe.Exists(ctx, lockK)
	n := pipe.Incr(ctx, attK)
	pipe.ExpireNX(ctx, attK, attemptsTTL)
	stored := pipe.Get(ctx, otpK)
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return 0, 0, err
	}
	if locked.Val() > 0 {
		return otpLocked, 0, nil
	}
	if errors.Is(stored.Err(), redis.Nil) {
		rc.Decr(ctx, attK) // not a guess at a live OTP
		return otpMissing, 0, nil
	}
	// The counter was taken before comparing, so attempt n is this guess.
	if stored.Val() == candidateHash && n.Val() <= int64(maxAttempts) {
		rc.Del(ctx, otpK, attK)
		return otpMatch, 0, nil
	}
	if n.Val() >= int64(maxAttempts) {
		pipe := rc.TxPipeline()
		pipe.Set(ctx, lockK, "1", lockTTL)
		pipe.Del(ctx, otpK, attK)
		if _, err := pipe.Exec(ctx); err != nil {
			return 0, 0, err
		}
		return otpLocked, int(n.Val()), nil
	}
	return otpWrong, int(n.Val()), nil
}

func isConnError(err error) bool {
	var re redis.Error
	// Server replies (e.g. "ERR unknown command 'EVALSHA'") are redis.Error;
	// anything else is the network.
	return !errors.As(err, &re)
}
