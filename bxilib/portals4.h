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
#ifndef _PORTALS_4_H
#define _PORTALS_4_H

#ifndef __KERNEL__
#include <stdint.h>
#else
#include <linux/types.h>
#endif

#include <linux/bxi3/ptl.h>

/*
 * API version
 */
#define PTL_MAJOR_VERSION 4
#define PTL_MINOR_VERSION 3

/*
 * Scalar types
 */
typedef void *ptl_cpu_addr_t;
#ifdef __KERNEL__
typedef dma_addr_t ptl_addr_t;
#else
typedef void *ptl_addr_t;
#endif
typedef uint64_t ptl_hdr_data_t;
typedef uint8_t ptl_interface_t;
typedef uint64_t ptl_match_bits_t;
typedef uint32_t ptl_nid_t;
typedef uint16_t ptl_pid_t;
typedef uint32_t ptl_pt_index_t;
typedef uint32_t ptl_rank_t;
typedef uint64_t ptl_size_t;
typedef uint64_t ptl_sr_value_t;
typedef uint64_t ptl_time_t;
typedef uint32_t ptl_uid_t;

/* BXI Extension */
typedef uint8_t ptl_bxi_hdr_data_t;

/*
 * NI Features
 *
 * These values are defined SW only
 */
#define PTL_COHERENT_ATOMICS (1 << 0)
#define PTL_TARGET_BIND_INACCESSIBLE (1 << 1)
#define PTL_TOTAL_DATA_ORDERING (1 << 2)

struct ptl_ni_limits {
	int max_entries;
	int max_unexpected_headers;
	int max_mds;
	int max_cts;
	int max_eqs;
	int max_pt_index;
	int max_iovecs;
	int max_list_size;
	int max_triggered_ops;
	ptl_size_t max_msg_size;
	ptl_size_t max_atomic_size;
	ptl_size_t max_fetch_atomic_size;
	ptl_size_t max_waw_ordered_size;
	ptl_size_t max_war_ordered_size;
	ptl_size_t max_volatile_size;
	unsigned int features;
	uint16_t bxi_max_cqs;
	unsigned int bxi_compute_line;
	ptl_cq_mode_t cq_mode;
	ptl_size_t host_cq_size;
};
typedef struct ptl_ni_limits ptl_ni_limits_t;

typedef union {
	struct {
		ptl_nid_t nid;
		ptl_pid_t pid;
	} phys;
	ptl_rank_t rank;
} ptl_process_t;

/* Status registers */
typedef enum {
	PTL_SR_DROP_COUNT = 0,
	PTL_SR_OPERATION_VIOLATIONS = 2,
	PTL_SR_PERMISSION_VIOLATIONS = 1,
} ptl_sr_index_t;

/*
 * Atomic operations & datatypes
 *
 * TODO: no values are defined by HW yet
 */
typedef enum {
	PTL_BAND,
	PTL_BOR,
	PTL_BXOR,
	PTL_CSWAP,
	PTL_CSWAP_GE,
	PTL_CSWAP_GT,
	PTL_CSWAP_LE,
	PTL_CSWAP_LT,
	PTL_CSWAP_NE,
	PTL_DIFF,
	PTL_LAND,
	PTL_LOR,
	PTL_LXOR,
	PTL_MAX,
	PTL_MIN,
	PTL_MSWAP,
	PTL_PROD,
	PTL_SUM,
	PTL_SWAP,
} ptl_op_t;

typedef enum {
	PTL_DOUBLE,
	PTL_DOUBLE_COMPLEX,
	PTL_FLOAT,
	PTL_FLOAT_COMPLEX,
	PTL_INT16_T,
	PTL_INT32_T,
	PTL_INT64_T,
	PTL_INT8_T,
	PTL_LONG_DOUBLE,
	PTL_LONG_DOUBLE_COMPLEX,
	PTL_UINT16_T,
	PTL_UINT32_T,
	PTL_UINT64_T,
	PTL_UINT8_T,
} ptl_datatype_t;

/*
 * Handle types
 *
 * SW handles are not the same as HW handles
 */
typedef struct {
	uint8_t priv[8];
} ptl_handle_any_t;
typedef ptl_handle_any_t ptl_handle_ct_t;
typedef ptl_handle_any_t ptl_handle_eq_t;
typedef ptl_handle_any_t ptl_handle_le_t;
typedef ptl_handle_any_t ptl_handle_md_t;
typedef ptl_handle_any_t ptl_handle_me_t;
typedef ptl_handle_any_t ptl_handle_ni_t;

#define PTL_INVALID_HANDLE ((ptl_handle_any_t){ .priv = { 0 } })
#define PTL_EQ_NONE ((ptl_handle_any_t){ .priv = { 0 } })
#define PTL_CT_NONE ((ptl_handle_any_t){ .priv = { 0 } })

/*
 * PT Options
 *
 * HAS: Table 53, section 4.4.3.2
 */

#define PTL_PT_ANY ((1 << 21) + 1)

#define PTL_PT_ONLY_USE_ONCE (1 << 0)
#define PTL_PT_ONLY_TRUNCATE (1 << 1)
#define PTL_PT_FLOWCTRL (1 << 2)
#define PTL_PT_ALLOC_DISABLED (1 << 3)

/*
 * MD Options
 *
 * HAS: Table 45, section 4.4.3.2
 */
#define PTL_MD_EVENT_SEND_DISABLE (1 << 0)
#define PTL_MD_EVENT_SUCCESS_DISABLE (1 << 1)
#define PTL_MD_EVENT_CT_SEND (1 << 2)
#define PTL_MD_EVENT_CT_REPLY (1 << 3)
#define PTL_MD_EVENT_CT_ACK (1 << 4)
#define PTL_MD_EVENT_CT_BYTES (1 << 5)
#define PTL_MD_UNORDERED (1 << 6)
#define PTL_MD_UNRELIABLE (1 << 8)
#define PTL_MD_VOLATILE (1 << 9)

typedef struct {
	ptl_addr_t start;
#ifdef __KERNEL__
	ptl_cpu_addr_t cpu_start;
#endif
	ptl_size_t length;
	unsigned int options;
	ptl_handle_eq_t eq_handle;
	ptl_handle_ct_t ct_handle;

	/* BXI extension */
	uint16_t bxi_cq;
} ptl_md_t;

typedef struct {
	ptl_addr_t start;
#ifdef __KERNEL__
	ptl_cpu_addr_t cpu_start;
#endif
	ptl_size_t length;
	ptl_handle_ct_t ct_handle;
	ptl_uid_t uid;
	unsigned int options;
	ptl_process_t match_id;
	ptl_match_bits_t match_bits;
	ptl_match_bits_t ignore_bits;
	ptl_size_t min_free;

	/* BXI extension */
	uint16_t bxi_cq;
} ptl_me_t;
typedef ptl_me_t ptl_le_t;

typedef enum {
	PTL_SEARCH_ONLY = 0,
	PTL_SEARCH_DELETE = 1,
} ptl_search_op_t;

/*
 * Acknowledgment modes
 *
 * HAS: Table 4, section 4.2.7
 */
typedef enum {
	PTL_NO_ACK_REQ = 0,
	PTL_CT_ACK_REQ = 1,
	PTL_OC_ACK_REQ = 2,
	PTL_ACK_REQ = 3,
} ptl_ack_req_t;

/*
 * Flow control error codes
 * HAS 0.95 FUNCTIONAL, BXI3-28015
 */
typedef enum {
	PTL_FC_ERR_NO_ERROR = 0x0,
	PTL_FC_ERR_NO_MATCH = 0x1,
	PTL_FC_ERR_EQ_RESERVATION_FAILURE = 0x2,
	PTL_FC_ERR_UH_ALLOCATION_FAILURE = 0x3,
} ptl_fc_err_t;

typedef struct {
	ptl_addr_t start;
	void *user_ptr;
	ptl_hdr_data_t hdr_data;
	ptl_bxi_hdr_data_t bxi_hdr_data;
	ptl_match_bits_t match_bits;
	ptl_size_t rlength, mlength, remote_offset;
	ptl_uid_t uid;
	ptl_process_t initiator;
	ptl_event_kind_t type;
	ptl_list_t ptl_list;
	ptl_pt_index_t pt_index;
	ptl_ni_fail_t ni_fail_type;
	ptl_op_t atomic_operation;
	ptl_datatype_t atomic_type;
	ptl_fc_err_t fc_err;
} ptl_event_t;

typedef struct {
	ptl_size_t success;
	ptl_size_t failure;
} ptl_ct_event_t;

/*
 * Miscellaneous constants
 *
 * HAS: Mostly Table 4, section 4.2.7
 */
#define PTL_IFACE_DEFAULT 0
/* HAS: Table 45 and Table 50, section 4.4.3.2 */
#define PTL_IOVEC (1 << 7)
#define PTL_NID_ANY 0x3fffffff

/* SW only */
#define PTL_RANK_ANY (~((uint32_t)0))
#define PTL_TIME_FOREVER (~0ULL)
/*
 * In the portals spec sizes can be a number of entry, an offset, a buffer length, ...
 * This define is the largest size acceptable from any of the functions, but some functions
 * may not accept as much as this.
 */
#define PTL_SIZE_MAX ((1ULL << 57) - 1)

/*
 * Miscellaneous structures
 */
typedef struct {
	ptl_addr_t iov_base;
	ptl_size_t iov_len;
} ptl_iovec_t;

int PtlInit(void);
void PtlFini(void);
void PtlAbort(void);

int PtlNIInit(ptl_interface_t iface, unsigned int options, ptl_pid_t pid,
	      const ptl_ni_limits_t *desired, ptl_ni_limits_t *actual, ptl_handle_ni_t *ni_handle);
int PtlNIFini(ptl_handle_ni_t ni_handle);
int PtlNIHandle(ptl_handle_any_t handle, ptl_handle_ni_t *ni_handle);
int PtlNIStatus(ptl_handle_ni_t ni_handle, ptl_sr_index_t status_register, ptl_sr_value_t *status);
int PtlSetMap(ptl_handle_ni_t ni_handle, ptl_size_t map_size, const ptl_process_t *mapping);
int PtlGetMap(ptl_handle_ni_t ni_handle, ptl_size_t map_size, ptl_process_t *mapping,
	      ptl_size_t *actual_map_size);

int PtlPTAlloc(ptl_handle_ni_t ni_handle, unsigned int options, ptl_handle_eq_t eq_handle,
	       ptl_pt_index_t pt_index_req, ptl_pt_index_t *pt_index);
int PtlPTFree(ptl_handle_ni_t ni_handle, ptl_pt_index_t pt_index);
int PtlPTEnable(ptl_handle_ni_t ni_handle, ptl_pt_index_t pt_index);
int PtlPTDisable(ptl_handle_ni_t ni_handle, ptl_pt_index_t pt_index);

int PtlGetUid(ptl_handle_ni_t ni_handle, ptl_uid_t *uid);
int PtlGetId(ptl_handle_ni_t ni_handle, ptl_process_t *id);
int PtlGetPhysId(ptl_handle_ni_t ni_handle, ptl_process_t *id);

int PtlMDBind(ptl_handle_ni_t ni_handle, const ptl_md_t *md, ptl_handle_md_t *md_handle);
int PtlMDRelease(ptl_handle_md_t md_handle);

int PtlLEAppend(ptl_handle_ni_t ni_handle, ptl_pt_index_t pt_index, const ptl_le_t *le,
		ptl_list_t ptl_list, void *user_ptr, ptl_handle_le_t *le_handle);
int PtlLEUnlink(ptl_handle_le_t le_handle);
int PtlLESearch(ptl_handle_ni_t ni_handle, ptl_pt_index_t pt_index, const ptl_le_t *le,
		ptl_search_op_t ptl_search_op, void *user_ptr);
int PtlMEAppend(ptl_handle_ni_t ni_handle, ptl_pt_index_t pt_index, const ptl_me_t *me,
		ptl_list_t ptl_list, void *user_ptr, ptl_handle_me_t *me_handle);
int PtlMEUnlink(ptl_handle_me_t me_handle);
int PtlMESearch(ptl_handle_ni_t ni_handle, ptl_pt_index_t pt_index, const ptl_me_t *me,
		ptl_search_op_t ptl_search_op, void *user_ptr);

int PtlEQAlloc(ptl_handle_ni_t ni_handle, ptl_size_t count, ptl_handle_eq_t *eq_handle);
int PtlEQFree(ptl_handle_eq_t eq_handle);
int PtlEQGet(ptl_handle_eq_t eq_handle, ptl_event_t *event);
int PtlEQWait(ptl_handle_eq_t eq_handle, ptl_event_t *event);
int PtlEQPoll(const ptl_handle_eq_t *eq_handles, unsigned int size, ptl_time_t timeout,
	      ptl_event_t *event, unsigned int *which);

int PtlCTAlloc(ptl_handle_ni_t ni_handle, ptl_handle_ct_t *ct_handle);
int PtlCTFree(ptl_handle_ct_t ct_handle);
int PtlCTCancelTriggered(ptl_handle_ct_t ct_handle);
int PtlCTGet(ptl_handle_ct_t ct_handle, ptl_ct_event_t *event);
int PtlCTWait(ptl_handle_ct_t ct_handle, ptl_size_t test, ptl_ct_event_t *event);
int PtlCTPoll(const ptl_handle_ct_t *ct_handles, const ptl_size_t *tests, unsigned int size,
	      ptl_time_t timeout, ptl_ct_event_t *event, unsigned int *which);
int PtlCTSet(ptl_handle_ct_t ct_handle, ptl_ct_event_t new_ct);
int PtlCTInc(ptl_handle_ct_t ct_handle, ptl_ct_event_t increment);

int PtlPut(ptl_handle_md_t md_handle, ptl_size_t local_offset, ptl_size_t length,
	   ptl_ack_req_t ack_req, ptl_process_t target_id, ptl_pt_index_t pt_index,
	   ptl_match_bits_t match_bits, ptl_size_t remote_offset, void *user_ptr,
	   ptl_hdr_data_t hdr_data);
int PtlGet(ptl_handle_md_t md_handle, ptl_size_t local_offset, ptl_size_t length,
	   ptl_process_t target_id, ptl_pt_index_t pt_index, ptl_match_bits_t match_bits,
	   ptl_size_t remote_offset, void *user_ptr);
int PtlAtomic(ptl_handle_md_t md_handle, ptl_size_t local_offset, ptl_size_t length,
	      ptl_ack_req_t ack_req, ptl_process_t target_id, ptl_pt_index_t pt_index,
	      ptl_match_bits_t match_bits, ptl_size_t remote_offset, void *user_ptr,
	      ptl_hdr_data_t hdr_data, ptl_op_t operation, ptl_datatype_t datatype);
int PtlFetchAtomic(ptl_handle_md_t get_md_handle, ptl_size_t local_get_offset,
		   ptl_handle_md_t put_md_handle, ptl_size_t local_put_offset, ptl_size_t length,
		   ptl_process_t target_id, ptl_pt_index_t pt_index, ptl_match_bits_t match_bits,
		   ptl_size_t remote_offset, void *user_ptr, ptl_hdr_data_t hdr_data,
		   ptl_op_t operation, ptl_datatype_t datatype);
int PtlSwap(ptl_handle_md_t get_md_handle, ptl_size_t local_get_offset,
	    ptl_handle_md_t put_md_handle, ptl_size_t local_put_offset, ptl_size_t length,
	    ptl_process_t target_id, ptl_pt_index_t pt_index, ptl_match_bits_t match_bits,
	    ptl_size_t remote_offset, void *user_ptr, ptl_hdr_data_t hdr_data, const void *operand,
	    ptl_op_t operation, ptl_datatype_t datatype);
int PtlAtomicSync(void);

int PtlTriggeredPut(ptl_handle_md_t md_handle, ptl_size_t local_offset, ptl_size_t length,
		    ptl_ack_req_t ack_req, ptl_process_t target_id, ptl_pt_index_t pt_index,
		    ptl_match_bits_t match_bits, ptl_size_t remote_offset, void *user_ptr,
		    ptl_hdr_data_t hdr_data, ptl_handle_ct_t trig_ct_handle, ptl_size_t threshold);
int PtlTriggeredGet(ptl_handle_md_t md_handle, ptl_size_t local_offset, ptl_size_t length,
		    ptl_process_t target_id, ptl_pt_index_t pt_index, ptl_match_bits_t match_bits,
		    ptl_size_t remote_offset, void *user_ptr, ptl_handle_ct_t ct_handle,
		    ptl_size_t threshold);
int PtlTriggeredAtomic(ptl_handle_md_t md_handle, ptl_size_t local_offset, ptl_size_t length,
		       ptl_ack_req_t ack_req, ptl_process_t target_id, ptl_pt_index_t pt_index,
		       ptl_match_bits_t match_bits, ptl_size_t remote_offset, void *user_ptr,
		       ptl_hdr_data_t hdr_data, ptl_op_t operation, ptl_datatype_t datatype,
		       ptl_handle_ct_t trig_ct_handle, ptl_size_t threshold);
int PtlTriggeredFetchAtomic(ptl_handle_md_t get_md_handle, ptl_size_t local_get_offset,
			    ptl_handle_md_t put_md_handle, ptl_size_t local_put_offset,
			    ptl_size_t length, ptl_process_t target_id, ptl_pt_index_t pt_index,
			    ptl_match_bits_t match_bits, ptl_size_t remote_offset, void *user_ptr,
			    ptl_hdr_data_t hdr_data, ptl_op_t operation, ptl_datatype_t datatype,
			    ptl_handle_ct_t trig_ct_handle, ptl_size_t threshold);
int PtlTriggeredSwap(ptl_handle_md_t get_md_handle, ptl_size_t local_get_offset,
		     ptl_handle_md_t put_md_handle, ptl_size_t local_put_offset, ptl_size_t length,
		     ptl_process_t target_id, ptl_pt_index_t pt_index, ptl_match_bits_t match_bits,
		     ptl_size_t remote_offset, void *user_ptr, ptl_hdr_data_t hdr_data,
		     const void *operand, ptl_op_t operation, ptl_datatype_t datatype,
		     ptl_handle_ct_t trig_ct_handle, ptl_size_t threshold);
int PtlTriggeredCTSet(ptl_handle_ct_t ct_handle, ptl_ct_event_t new_ct,
		      ptl_handle_ct_t trig_ct_handle, ptl_size_t threshold);
int PtlTriggeredCTInc(ptl_handle_ct_t ct_handle, ptl_ct_event_t increment,
		      ptl_handle_ct_t trig_ct_handle, ptl_size_t threshold);

int PtlStartBundle(ptl_handle_ni_t ni_handle);
int PtlEndBundle(ptl_handle_ni_t ni_handle);
int PtlHandleIsEqual(ptl_handle_any_t handle1, ptl_handle_any_t handle2);

/*
 * Deprecated Values
 */

#define PTL_INTERRUPTED 0x200

#endif /* _PORTALS_4_H */
