// Copyright 2026 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

#include <time.h>

#include "_cgo_export.h"

int
fast_add(int a, int b)
{
	return a + b;
}

int
slow_noescape(int a, int b)
{
	return a + b;
}

int
slow_nocallback(int a, int b)
{
	return a + b;
}

static int
do_sleep(int micros)
{
	struct timespec ts;
	ts.tv_sec = micros / 1000000;
	ts.tv_nsec = (long)(micros % 1000000) * 1000L;
	nanosleep(&ts, 0);
	return micros;
}

int
block_sleep(int micros)
{
	return do_sleep(micros);
}

int
plain_sleep(int micros)
{
	return do_sleep(micros);
}

int
read_int(int *p)
{
	return *p;
}

int
sum_bytes(unsigned char *p, int n)
{
	int s = 0;
	for (int i = 0; i < n; i++) {
		s += p[i];
	}
	return s;
}

static unsigned long long
do_spin(unsigned long long iters)
{
	unsigned long long x = 0;
	for (unsigned long long i = 0; i < iters; i++) {
		x += i * 6364136223846793005ULL + 1442695040888963407ULL;
	}
	return x;
}

unsigned long long
busy_spin(unsigned long long iters)
{
	return do_spin(iters);
}

unsigned long long
plain_spin(unsigned long long iters)
{
	return do_spin(iters);
}

int
cb_fire(void)
{
	return GoCallback(41);
}

enum {
	watch_ok = 0,
	watch_value = 1,
	watch_pointer = 2
};

int
watch_spin(unsigned char *p, unsigned char want, int micros)
{
	struct timespec ts;
	volatile unsigned char *vp = (volatile unsigned char *)p;
	volatile unsigned char *entry = vp;
	unsigned long long n = 0;
	long start, limit, now;

	if (micros > 300000) {
		micros = 300000;
	}
	clock_gettime(CLOCK_MONOTONIC, &ts);
	start = ts.tv_sec * 1000000000L + ts.tv_nsec;
	limit = (long)micros * 1000L;
	for (;;) {
		if (*vp != want) {
			return watch_value;
		}
		if (vp != entry) {
			return watch_pointer;
		}
		if ((n & 1023) == 0) {
			clock_gettime(CLOCK_MONOTONIC, &ts);
			now = ts.tv_sec * 1000000000L + ts.tv_nsec - start;
			if (now >= limit) {
				return watch_ok;
			}
		}
		n++;
	}
}

int
plain_cb_fire(void)
{
	return GoCallback(7);
}
