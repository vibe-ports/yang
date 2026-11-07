/* SPDX-License-Identifier: BSD-3-Clause */
#ifndef NATIVE_REGISTRATION_OBSERVER_H
#define NATIVE_REGISTRATION_OBSERVER_H
void nr_event(const char *event, const void *node, const void *table, int rc, const void *source);
void nr_occurrence(const void *table, unsigned slot, const void *value, unsigned hash, int insert);
void nr_working_rb(const void *root);
void nr_remote(const void *first, const void *sibling, const void *node);
#endif
