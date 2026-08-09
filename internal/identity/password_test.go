package identity

import (
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

var fastTestParams = Argon2idParams{
	MemoryKiB:   8 * 1024,
	Iterations:  1,
	Parallelism: 1,
	SaltLength:  16,
	KeyLength:   32,
}

func TestArgon2idRoundTripAndRandomSalt(t *testing.T) {
	first, err := HashPassword("correct horse battery staple", fastTestParams)
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword("correct horse battery staple", fastTestParams)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("two password hashes reused the same salt")
	}
	if !strings.HasPrefix(first, "$argon2id$v=19$m=8192,t=1,p=1$") {
		t.Fatalf("hash = %q, want an Argon2id PHC string with recorded parameters", first)
	}

	ok, err := VerifyPassword(first, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("VerifyPassword(correct) = %v, %v", ok, err)
	}
	ok, err = VerifyPassword(first, "wrong password")
	if err != nil || ok {
		t.Fatalf("VerifyPassword(wrong) = %v, %v", ok, err)
	}
}

func TestVerifyPasswordRejectsMalformedOrUnsafePHCParameters(t *testing.T) {
	tests := []string{
		"not-a-phc-string",
		"$argon2i$v=19$m=8192,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		"$argon2id$v=18$m=8192,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		"$argon2id$v=19$m=1048577,t=1,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
		"$argon2id$v=19$m=8192,t=0,p=1$c2FsdHNhbHRzYWx0c2FsdA$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhc2g",
	}
	for _, encoded := range tests {
		t.Run(encoded, func(t *testing.T) {
			if ok, err := VerifyPassword(encoded, "password"); err == nil || ok {
				t.Fatalf("VerifyPassword(%q) = %v, %v; want malformed-hash error", encoded, ok, err)
			}
		})
	}
}

func TestHashPasswordValidatesInputAndParameters(t *testing.T) {
	if _, err := HashPassword("", fastTestParams); err == nil {
		t.Fatal("empty password was accepted")
	}
	unsafe := fastTestParams
	unsafe.MemoryKiB = 1024
	if _, err := HashPassword("password", unsafe); err == nil {
		t.Fatal("unsafe Argon2 memory cost was accepted")
	}
}

func TestArgon2idDerivationHonorsGlobalConcurrencyLimit(t *testing.T) {
	encoded, err := HashPassword("correct horse battery staple", fastTestParams)
	if err != nil {
		t.Fatal(err)
	}
	original := argon2IDKey
	defer func() { argon2IDKey = original }()

	var active atomic.Int32
	var maximum atomic.Int32
	started := make(chan struct{}, argon2ConcurrencyLimit+4)
	release := make(chan struct{})
	argon2IDKey = func(_ []byte, _ []byte, _ uint32, _ uint32, _ uint8, keyLength uint32) []byte {
		current := active.Add(1)
		for {
			observed := maximum.Load()
			if current <= observed || maximum.CompareAndSwap(observed, current) {
				break
			}
		}
		started <- struct{}{}
		<-release
		active.Add(-1)
		return make([]byte, keyLength)
	}

	const workers = argon2ConcurrencyLimit + 2
	var waitGroup sync.WaitGroup
	waitGroup.Add(workers)
	for index := 0; index < workers; index++ {
		go func(index int) {
			defer waitGroup.Done()
			if index%2 == 0 {
				_, _ = HashPassword("password for concurrency test", fastTestParams)
				return
			}
			_, _ = VerifyPassword(encoded, "correct horse battery staple")
		}(index)
	}

	reachedLimit := true
	for index := 0; index < argon2ConcurrencyLimit; index++ {
		select {
		case <-started:
		case <-time.After(time.Second):
			reachedLimit = false
		}
	}
	extraStarted := false
	select {
	case <-started:
		extraStarted = true
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	waitGroup.Wait()
	if !reachedLimit {
		t.Fatal("the configured Argon2 concurrency limit was not reached")
	}
	if extraStarted {
		t.Fatalf("more than %d Argon2 derivations entered the critical section", argon2ConcurrencyLimit)
	}
	if got := maximum.Load(); got > argon2ConcurrencyLimit {
		t.Fatalf("maximum concurrent Argon2 derivations = %d, want <= %d", got, argon2ConcurrencyLimit)
	}
}
