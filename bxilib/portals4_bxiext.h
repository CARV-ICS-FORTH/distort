/*
 * Copyright (C) Bull S.A.S - 2023
 *
 * This program is free software; you can redistribute it and/or modify it under the terms of the
 * GNU General Public License as published by the Free Software Foundation; either version 2 of the
 * License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful, but WITHOUT ANY WARRANTY; without
 * even the implied warranty of MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the GNU
 * General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License along with this program; if
 * not, write to the Free Software Foundation, Inc., 51 Franklin Street, Fifth Floor, Boston, MA
 * 02110-1301, USA.
 *
 * BXI Low Level Team <dl-bxi-sw-ll@atos.net>
 *
 */

#ifndef _PORTALS_4_BXIEXT_H
#define _PORTALS_4_BXIEXT_H

#ifndef __KERNEL__
#include <stdbool.h>
#include <stdatomic.h>
#include <stddef.h>
#else
#include <linux/types.h>
#include <linux/cpumask.h>
#endif
#include "portals4.h"

#define PTL_STATS_VERSION 3

#ifdef __KERNEL__
typedef atomic64_t atomic_ulong_t;
#else
typedef atomic_ulong atomic_ulong_t;
#endif

/* BXI Specific return codes */
#define PTL_UNIMPLEMENTED 0x300

enum ptl_str_type {
	PTL_STR_ERROR, /* Return codes */
	PTL_STR_EVENT, /* Events */
	PTL_STR_FAIL_TYPE, /* Failure type */
	PTL_STR_ATOMIC_OP, /* Atomic operation types */
	PTL_STR_ATOMIC_TYPE, /* Atomic data type */
	PTL_STR_ACK_TYPE, /* ACK req type */
	PTL_STR_FC_ERR_CODES, /* Flow control error codes */
	PTL_STR_SEARCH_MODE, /* Search mode */
};
const char *PtlToStr(int rc, enum ptl_str_type type);

enum ptl_opts_str_type {
	PTL_OPTS_STR_MD,
	PTL_OPTS_STR_LE,
	PTL_OPTS_STR_ME,
	PTL_OPTS_STR_PT,
};

int PtlOptsToStr(char *dest, size_t dest_len, unsigned int options, enum ptl_opts_str_type type);

/*
 * Statistics
 */

typedef struct {
	uint64_t magic;
	int version;
	bool *print;
	/* API calls counters */
	struct ptl_api_calls {
		atomic_ulong_t init;
		atomic_ulong_t fini;
		atomic_ulong_t ni_init;
		atomic_ulong_t ni_fini;

		atomic_ulong_t md_bind;
		atomic_ulong_t md_release;

		atomic_ulong_t me_append;
		atomic_ulong_t me_unlink;
		atomic_ulong_t me_search;

		atomic_ulong_t le_append;
		atomic_ulong_t le_unlink;
		atomic_ulong_t le_search;

		atomic_ulong_t eq_allocasync;
		atomic_ulong_t eq_alloc;
		atomic_ulong_t eq_free;
		atomic_ulong_t eq_get;
		atomic_ulong_t eq_poll;
		atomic_ulong_t eq_wait;

		atomic_ulong_t ct_alloc;
		atomic_ulong_t ct_free;
		atomic_ulong_t ct_get;
		atomic_ulong_t ct_set;
		atomic_ulong_t ct_poll;
		atomic_ulong_t ct_wait;
		atomic_ulong_t ct_inc;

		atomic_ulong_t pt_alloc;
		atomic_ulong_t pt_free;
		atomic_ulong_t pt_enable;
		atomic_ulong_t pt_disable;

		atomic_ulong_t atomic_sync;

		atomic_ulong_t put;
		atomic_ulong_t msg_put;

		atomic_ulong_t get;
		atomic_ulong_t msg_get;

		atomic_ulong_t swap;
		atomic_ulong_t atomic;
		atomic_ulong_t fetch_atomic;
		atomic_ulong_t ct_cancel_triggered;
		atomic_ulong_t triggered_put;
		atomic_ulong_t triggered_get;
		atomic_ulong_t triggered_atomic;
		atomic_ulong_t triggered_fetch_atomic;
		atomic_ulong_t triggered_swap;
		atomic_ulong_t triggered_ct_set;
		atomic_ulong_t triggered_ct_inc;
	} api;

	/* Resource utilization counters */
	struct ptl_resource_util {
		atomic_ulong_t pio_put;
		atomic_ulong_t cq_mode_switch;
	} res;
} ptl_stats_t;

void PtlGetStatistics(ptl_handle_ni_t nih, ptl_stats_t *stats);

/* Temporary: workaround to not check limits */
extern bool ptl_no_limits_check_workaround;

extern bool ptl_force_debug;

/*
 * Source Compatibility with BXIV2
 */

#define PTL_ME_MANAGE_LOCAL_STOP_IF_UH 0
#define PTL_ME_OV_RDV_PUT_ONLY 0
#define PTL_ME_OV_RDV_PUT_DISABLE 0
#define PTL_SIZE_INVALID 0x300
#define PTL_TRY_AGAIN 0x301

#define PTL_BXI3_DEFAULT_CQ 0

/*
 * BXI Specific resouce options
 * HW does not use these bits for features
 */

#define PTL_BXI3_ASYNC_RELEASE (1 << 29)
#define PTL_BXI3_CQ (1 << 30)

/*
 * BXI Specific Portals Features
 */

#define PTL_BXI3_MULTI_CQ (1 << 3)
#define PTL_BXI3_SERVICE (1 << 4)

/*
 * Enables PtlNIInit to return multiple pids for the same interface
 * (breaking the Portals behaviour)
 */
#define PTL_BXI3_MULTIPLE_PIDS (1 << 5)
/*
 * Debug mode
 * Active some library checks and wait for BXI responses after command posting.
 */
#define PTL_BXI3_DEBUG (1 << 6)
/* Specify a compute line through ptl_ni_limits_t::compute_line */
#define PTL_BXI3_COMPUTE_LINE (1 << 7)
/* Specify the type of CQ to be used */
#define PTL_BXI3_CQ_MODE (1 << 8)
/* Enable the use of PTL_LE_MANAGE_LOCAL and PTL_LE_MAY_ALIGN options */
#define PTL_BXI3_LE_EXTENSION (1 << 9)

/*
 * BXI environment variables for Portals
 */

/*
 * Enable debug mode (see PTL_BXI3_DEBUG feature)
 */
#define BXI_DEBUG_ENV "BXI_DEBUG"

/*
 * Maximum length (not including the terminating null byte) of the string representation of a
 * Portals NID: <cluster id>:<hardware nid>:<VF id>
 */
#define PTL_BXI3_NID_STR_MAX (3 + 1 + 5 + 1 + 3)

#define PTL_BXI3_MAKE_NID(cluster_id, hw_nid, vfid)                                                \
	((ptl_nid_t)(((cluster_id) & 0x7f) << 23 | ((hw_nid) & 0xffff) << 7 | ((vfid) & 0x7f)))

int PtlStrToNid(ptl_handle_ni_t nih, const char *str, ptl_nid_t *nid);
const char *PtlNidToStr(ptl_nid_t nid, char *str, size_t size);

bool PtlIsASimulator(ptl_handle_ni_t nih);

/*
 * Kernelspace client code can use this function to get a pointer to the
 * driver's device struct associated with a NI handle. This is required
 * to allocate or map memory for DMA operations.
 */
#ifdef __KERNEL__
struct device *PtlGetDriverDev(ptl_handle_ni_t nih);
#endif

#ifdef __KERNEL__
typedef uint16_t ptl_eq_intr_index_t;

int PtlEQAsyncIntrAlloc(ptl_handle_ni_t nih, const cpumask_t *m, ptl_eq_intr_index_t *intr_index);
int PtlEQAsyncIntrFree(ptl_handle_ni_t nih, ptl_eq_intr_index_t intr_index);

int PtlEQAllocAsync(ptl_handle_ni_t nih, ptl_size_t size, ptl_handle_eq_t *reqh,
		    void (*cb)(void *arg, ptl_handle_eq_t eqh), void *arg,
		    ptl_eq_intr_index_t intr_index);
#endif

typedef struct {
	unsigned options;
	ptl_size_t local_offset; /* Ignored by PtlMsgPutOnce and PtlMsgGetOnce */
	ptl_size_t length; /* Ignored by PtlMsgPutOnce and PtlMsgGetOnce */
	ptl_process_t target_id;
	ptl_pt_index_t pt_index;
	ptl_match_bits_t match_bits;
	ptl_size_t remote_offset;
	void *user_ptr;
	ptl_hdr_data_t hdr_data;
	ptl_bxi_hdr_data_t bxi_hdr_data;
	ptl_ack_req_t ack_req;
} ptl_msg_t;

/* Flags for ptl_msg_t::options */
#define PTL_MSG_VOLATILE 1

int PtlMsgPut(ptl_handle_md_t mdh, const ptl_msg_t *msg);
int PtlMsgGet(ptl_handle_md_t mdh, const ptl_msg_t *msg);

int PtlMsgPutOnce(ptl_handle_ni_t ni_handle, const ptl_md_t *md, const ptl_msg_t *msg);
int PtlMsgGetOnce(ptl_handle_ni_t ni_handle, const ptl_md_t *md, const ptl_msg_t *msg);

#endif /* _PORTALS_4_BXIEXT_H */
