package auth

import (
	"container/list"
	"sync"
	"time"
)

const (
	adminLoginMaxAttempts = 5
	adminLoginWindow      = 5 * time.Minute
	adminLoginTrackedKeys = 2048
)

var adminLoginLimiter = newLoginAttemptLimiter(
	adminLoginMaxAttempts,
	adminLoginWindow,
	adminLoginTrackedKeys,
	time.Now,
)

type loginAttemptWindow struct {
	key       string
	attempts  int
	startedAt time.Time
}

type loginAttemptLimiter struct {
	mu          sync.Mutex
	maxAttempts int
	window      time.Duration
	maxEntries  int
	now         func() time.Time
	entries     map[string]*list.Element
	windows     *list.List
}

func newLoginAttemptLimiter(maxAttempts int, window time.Duration, maxEntries int, now func() time.Time) *loginAttemptLimiter {
	if maxAttempts <= 0 || window <= 0 || maxEntries <= 0 || now == nil {
		panic("invalid login attempt limiter configuration")
	}

	return &loginAttemptLimiter{
		maxAttempts: maxAttempts,
		window:      window,
		maxEntries:  maxEntries,
		now:         now,
		entries:     make(map[string]*list.Element, maxEntries),
		windows:     list.New(),
	}
}

// reserve counts an attempt before password verification so blocked requests never reach bcrypt.
func (l *loginAttemptLimiter) reserve(key string) bool {
	now := l.now()

	l.mu.Lock()
	defer l.mu.Unlock()

	l.pruneExpired(now)

	if elem, ok := l.entries[key]; ok {
		attempt := elem.Value.(*loginAttemptWindow)
		if attempt.attempts >= l.maxAttempts {
			return false
		}

		attempt.attempts++

		return true
	}

	if len(l.entries) >= l.maxEntries {
		l.remove(l.windows.Front())
	}

	attempt := &loginAttemptWindow{
		key:       key,
		attempts:  1,
		startedAt: now,
	}
	l.entries[key] = l.windows.PushBack(attempt)

	return true
}

func (l *loginAttemptLimiter) clear(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if elem, ok := l.entries[key]; ok {
		l.remove(elem)
	}
}

func (l *loginAttemptLimiter) pruneExpired(now time.Time) {
	for elem := l.windows.Front(); elem != nil; elem = l.windows.Front() {
		attempt := elem.Value.(*loginAttemptWindow)
		if now.Before(attempt.startedAt.Add(l.window)) {
			return
		}

		l.remove(elem)
	}
}

func (l *loginAttemptLimiter) remove(elem *list.Element) {
	if elem == nil {
		return
	}

	attempt := elem.Value.(*loginAttemptWindow)
	delete(l.entries, attempt.key)
	l.windows.Remove(elem)
}

func loginLimiterKey(clientIP string, username string) string {
	return clientIP + "\x00" + username
}
