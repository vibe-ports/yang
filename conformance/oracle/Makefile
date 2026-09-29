# Native build (macOS brew libyang, or any prefix): make LIBYANG_PREFIX=/opt/homebrew
LIBYANG_PREFIX ?= /opt/homebrew
CFLAGS ?= -O2 -g -Wall -Wextra -Wno-unused-parameter -std=c11
CPPFLAGS += -I$(LIBYANG_PREFIX)/include -I.
LDFLAGS += -L$(LIBYANG_PREFIX)/lib -Wl,-rpath,$(LIBYANG_PREFIX)/lib
LDLIBS += -lyang -lm

lyoracle: lyoracle.c cjson/cJSON.c cjson/cJSON.h
	$(CC) $(CPPFLAGS) $(CFLAGS) -o $@ lyoracle.c cjson/cJSON.c $(LDFLAGS) $(LDLIBS)

clean:
	rm -rf lyoracle lyoracle.dSYM
.PHONY: clean
