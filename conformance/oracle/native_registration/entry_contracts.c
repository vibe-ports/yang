/* SPDX-License-Identifier: BSD-3-Clause */
/* Test-only entry probe: rejected requests must not reach the native lookup.
 * This is observable even when the native API would return NULL and the driver
 * would subsequently return 4 without emitting its buffered diagnostics.
 * Only driver calls are renamed; calls inside the pinned library are intact. */
#undef lys_find_child
#include <stdlib.h>
#include "libyang.h"

const struct lysc_node *nr_entry_find_child(const struct ly_ctx *ctx, const struct lysc_node *parent,
        const struct lys_module *mod, const char *mod_name, uint32_t mod_len, const char *name,
        uint32_t name_len, uint32_t options)
{
    if((!ctx && !parent && !mod) || (!mod && !mod_name) || (mod && mod_name) || !name)_Exit(101);
    return lys_find_child(ctx,parent,mod,mod_name,mod_len,name,name_len,options);
}
