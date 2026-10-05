# Native build (macOS brew libyang, or any prefix): make LIBYANG_PREFIX=/opt/homebrew
LIBYANG_PREFIX ?= /opt/homebrew
CFLAGS ?= -O2 -g -Wall -Wextra -Wno-unused-parameter -std=c11
CPPFLAGS += -I$(LIBYANG_PREFIX)/include -I.
LDFLAGS += -L$(LIBYANG_PREFIX)/lib -Wl,-rpath,$(LIBYANG_PREFIX)/lib
LDLIBS += -lyang -lm
# The checkout is shared by containers of different platforms (DEV_PLATFORM):
# a stamp per machine type rebuilds lyoracle when the platform changes.
ARCH := $(shell uname -sm | tr ' ' '-')

lyoracle: lyoracle.c cjson/cJSON.c cjson/cJSON.h .arch-$(ARCH)
	$(CC) $(CPPFLAGS) $(CFLAGS) -o $@ lyoracle.c cjson/cJSON.c $(LDFLAGS) $(LDLIBS)

.arch-$(ARCH):
	rm -f .arch-*
	touch $@

clean:
	rm -rf lyoracle lyoracle.dSYM .arch-*
.PHONY: clean
