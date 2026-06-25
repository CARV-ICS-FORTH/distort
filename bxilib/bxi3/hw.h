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

/*
 * Note: This file might exist in multiple directories of a repo because of
 * git submodules. Therefore, we must use regular header guards instead of
 * pragma once to avoid multiple definitions errors.
 */
#ifndef BXI_INCLUDE_HW_H
#define BXI_INCLUDE_HW_H

/* Maximum number of NICs on the same host */
#define BXI3_MAX_NICS 16

#include "hw_csr.h"

/*
 * Vendor and device id.
 */
#define BXI3_PCI_VENDOR_ID_BULL 0x119F
#define BXI3_DEVICE_ID_SIMULATOR_MASK 0x2000

#define BXI3_PCI_DEVICE_ID_POC_CQ 0x1200
#define BXI3_PCI_DEVICE_ID_RELEASE2 0x1201
#define BXI3_PCI_DEVICE_ID_RELEASE2_SIMULATOR                                                      \
	(BXI3_PCI_DEVICE_ID_RELEASE2 | BXI3_DEVICE_ID_SIMULATOR_MASK)
#define BXI3_PCI_DEVICE_ID_RELEASE3 0x1202
#define BXI3_PCI_DEVICE_ID_RELEASE5 0x1205
#define BXI3_PCI_SERIAL_DEVICE_ID 0x12a1
#define BXI3_PCI_DEVICE_ID_RELEASE3_SIMULATOR                                                      \
	(BXI3_PCI_DEVICE_ID_RELEASE3 | BXI3_DEVICE_ID_SIMULATOR_MASK)

/* Virtual BXI3 NIC version */
static const union nicia_csr_nhi_al_release_version VBXI3_RELEASE = {
	.fields.release_num_a = 6, /* NICIA release */
	.fields.release_num_b = 5, /* Release candidate */
	.fields.release_num_c = 0, /* Sub Release candidate */
};

#define bxi3_is_a_simulator(pdev) (pdev->device & BXI3_DEVICE_ID_SIMULATOR_MASK)

/*
 * Memory
 */
#define BXI3_PAGE_SHIFT 16
#define BXI3_PAGE_ALIGN (1u << BXI3_PAGE_SHIFT)
#define BXI3_PAGE_OFFSET_MASK ((1UL << BXI3_PAGE_SHIFT) - 1)

/*
 * BXI 3 BARs
 */
#define BXI3_BAR_CSRS 0
#define BXI3_BAR_CQS 2

/*
 * Command Queue
 */
#define BXI3_NBR_OF_CQS NICIA_CSR_NFRL_CQH_CQ_ENABLE_REPEAT
#define BXI3_NBR_OF_CQS_PER_BLOCK 32u
#define BXI3_NBR_OF_CQ_BLOCKS (BXI3_NBR_OF_CQS / BXI3_NBR_OF_CQS_PER_BLOCK)
#define BXI3_CQ_NBR_OF_SLOTS 16u
#define BXI3_CQ_SLOT_SIZE 64u
#define BXI3_CQ_PARTS_PER_SLOT (BXI3_CQ_SLOT_SIZE / sizeof(uint64_t))

#define BXI3_CQ_SLOT_PTR_MASK ((BXI3_CQ_NBR_OF_SLOTS * 2) - 1)
#define BXI_CQ_INVALID_CMD_CODE 0x3f

#define NICIA_HWPID_MAX_NB BXI3_NBR_OF_CQS

struct bxi3_cq_host_feedback {
	uint64_t host_ptr : 26;
	uint64_t : 6;
	uint64_t nic_ptr : 6;
	uint64_t : 25;
	uint64_t valid : 1; /* TODO: check the position of this bit */
} __attribute__((packed));
_Static_assert(sizeof(struct bxi3_cq_host_feedback) == 8,
	       "Host CQ feedback structure must be 8-bytes long");

/*
 * Page Walk Translation
 */
#define BXI3_PWT_CONTEXT_NB 330
#define BXI3_PWT_PER_SQ_SLOTS 8192 /* power of 2 greater than PWT_CONTEXT_NB * 16B */

#define BXI3_PWT_PER_DESC_CODE_WRITE (1 << 0)
#define BXI3_PWT_PER_DESC_CODE_SUPERVISOR (1 << 1)
#define BXI3_PWT_PER_DESC_CODE_RESERVED_BIT (1 << 2)
struct bxi3_pwt_per_request {
	uint64_t pid : 10; /* TODO: should be 9 bits */
	uint64_t : 2; /* TODO: should be 3 bits */
	uint64_t addr_12_56 : 45;
	uint64_t valid : 1;
	uint64_t desc_code : 3;
	uint64_t : 3;
	uint64_t hw_cookie : 16;
	uint64_t : 48;
} __attribute__((packed));
_Static_assert(sizeof(struct bxi3_pwt_per_request) == 16,
	       "PWT request structure must be 16-bytes long");

#define BXI3_PWT_PER_RSP_SUCCESS 1
#define BXI3_PWT_PER_RSP_ERROR 0
union bxi3_pwt_per_completion {
	struct {
		uint64_t valid : 1;
		uint64_t response_code : 1;
		uint64_t desc_code : 3;
		uint64_t : 11;
		uint64_t hw_cookie : 16;
	} __attribute__((packed)) fields;
	uint32_t csr;
};
_Static_assert(sizeof(union bxi3_pwt_per_completion) == 4,
	       "PWT completion structure must be 4-bytes long");

enum bxi3_pwt_inv_op {
	BXI3_PWT_INV_OP_FULL = 0b0,
	BXI3_PWT_INV_OP_PID_FULL = 0b1,
	BXI3_PWT_INV_OP_SINGLE = 0b11,
};

#define BXI3_PWT_NBR_OF_INV_CHANNELS 128
union bxi3_pwt_invalidation {
	struct {
		uint64_t pid : 10; /* TODO: should be 9 bits */
		uint64_t : 2; /* TODO: should be 3 bits */
		uint64_t addr_12_56 : 45;
		uint64_t op_code : 2;
		uint64_t : 5;
	} __attribute__((packed)) fields;
	struct {
		uint64_t lo : 32;
		uint64_t hi : 32;
	} __attribute__((packed)) parts;
};
_Static_assert(sizeof(union bxi3_pwt_invalidation) == 8, "Invalidation size must be 8-byte long");
/*
 * Virtualization
 */
#define BXI3_NBR_OF_VMS 64u

/*
 * CSR Macros
 * This macro must be used in  case the CSR contains only one field.
 */
#ifdef __KERNEL__

#include <asm/io.h>

#define csr_read(base, csr)                                                                        \
	(csr##_R /* Check that the CSR exists and is readable. */                                  \
		 ioread32(base + csr))

#define csr_read_idx(base, csr, idx)                                                               \
	(csr##_R /* Check that the CSR exists and is readable. */                                  \
		 ioread32(base + csr + (idx) * csr##_INCR))

#define csr_write(val, base, csr)                                                                  \
	(csr##_W /* Check that the CSR exists and is writable. */                                  \
		 iowrite32((val) & csr##_MASK, base + csr))

#define csr_write_idx(val, base, csr, idx)                                                         \
	(csr##_W /* Check that the CSR exists and is writable. */                                  \
		 iowrite32((val) & csr##_MASK, base + csr + (idx) * csr##_INCR))

static inline uint64_t __csr_read64(void __iomem *base, uint64_t csr)
{
	uint64_t high_val1;
	uint64_t csr_val;
	uint64_t high_val2;

	high_val1 = ioread32((uint8_t __iomem *)base + csr + 4);

	for (;;) {
		csr_val = ioread32((uint8_t __iomem *)base + csr);
		high_val2 = ioread32((uint8_t __iomem *)base + csr + 4);

		if (high_val1 == high_val2)
			break;

		high_val1 = high_val2;
	}

	return (high_val2 << 32) | csr_val;
}

#define csr_read64(base, csr)                                                                      \
	(csr##_R /* Check that the CSR exists and is readable. */                                  \
		 __csr_read64(base, csr))

/* Poll until a condition on a CSR evaluates to true or a timeout (in msecs) expires. */
#define csr_wait_cond(base, offset, var_type, var, timeout_ms, cond)                               \
	({                                                                                         \
		int __rc = -EIO;                                                                   \
		unsigned long __end = jiffies + msecs_to_jiffies(timeout_ms);                      \
		while (time_before(jiffies, __end)) {                                              \
			var_type var = { .csr = ioread32((base) + (offset)) };                     \
			if (cond) {                                                                \
				__rc = 0;                                                          \
				break;                                                             \
			}                                                                          \
			usleep_range(100, 200);                                                    \
		}                                                                                  \
		__rc;                                                                              \
	})

#else /* userspace csr access macros */

#if defined(__aarch64__)

static inline uint32_t __csr_read(const volatile uint32_t *addr)
{
	uint32_t ret;
	asm volatile("ldar %w0, [%1]" : "=r"(ret) : "r"(addr));
	asm volatile("dsb ld" ::: "memory");
	return ret;
}

static inline void __csr_write(uint32_t val, const volatile uint32_t *addr)
{
	asm volatile("dsb st" ::: "memory");
	asm volatile("str %w0, %1" : : "rZ"(val), "Qo"(*addr));
}

#elif defined(__x86_64__)

static inline uint32_t __csr_read(const volatile uint32_t *addr)
{
	uint32_t ret;
	asm volatile("movl %1,%0" : "=r"(ret) : "m"(*addr) : "memory");
	return ret;
}

static inline void __csr_write(uint32_t val, const volatile uint32_t *addr)
{
	asm volatile("movl %0,%1" : : "r"(val), "m"(*addr) : "memory");
}

#else
#error Unsupported architecture
#endif

#define csr_read(base, csr)                                                                        \
	(csr##_R /* Check that the CSR exists and is readable. */                                  \
		 __csr_read((uint32_t *)(base + csr)))

#define csr_write(val, base, csr)                                                                  \
	(csr##_W /* Check that the CSR exists and is writable. */                                  \
		 __csr_write((val) & csr##_MASK, (uint32_t *)(base + csr)))

#endif /* end of user space csr access macros */

/*
 * Portals ressources handle size, in bits
 * See table 42: Maximum counts of Portals resources in NICIA HAS V0.78
 */
#define NICIA_PTE_HANDLE_SIZE 21
#define NICIA_EQ_HANDLE_SIZE 22
#define NICIA_CT_HANDLE_SIZE 22
#define NICIA_TRIG_OP_HANDLE_SIZE 23
#define NICIA_MD_HANDLE_SIZE 25
#define NICIA_ME_HANDLE_SIZE 25
#define NICIA_UH_HANDLE_SIZE 25

struct bxi3_cq_slot {
	volatile uint64_t parts[BXI3_CQ_PARTS_PER_SLOT];
};

struct bxi3_cq_hw {
	struct bxi3_cq_slot slots[BXI3_CQ_NBR_OF_SLOTS];
	uint8_t _pad[BXI3_PAGE_ALIGN - sizeof(struct bxi3_cq_slot) * BXI3_CQ_NBR_OF_SLOTS];
};

/* Portals HW constants */

#define NICIA_EQ_NONE 0x3FFFFF
#define NICIA_CT_NONE 0x3FFFFF

/* Net constants */

#define NICIA_RX_MAGIC 0x4E49434941455654ULL /* "NICIAEVT" ("TVEAICIN" in little-endian) */

enum nicia_u_err {
	NICIA_U_ERR_BAD_CSR = 0x0,
	NICIA_U_ERR_BAD_CMD = 0x1,
	NICIA_U_ERR_BAD_NI = 0x2,
	NICIA_U_ERR_BAD_STRUCT = 0x3,
	NICIA_U_ERR_MEMORY_ACCESS = 0x5,
	NICIA_U_ERR_TRANSFER = 0x6,
	NICIA_U_ERR_LIST_CORRUPTION = 0x7,
	NICIA_U_ERR_TCPIP = 0x8,
};

/*
 * Common error descriptors
 */

struct nicia_u_err_node {
	uint64_t cluster_id : 7;
	uint64_t node_id : 16;
} __attribute__((packed));

struct nicia_u_err_process {
	uint64_t cluster_id : 7;
	uint64_t nid : 16;
	uint64_t vfid : 7;
	uint64_t pid : 10;
} __attribute__((packed));

/*
 * Instance codes
 */

enum nicia_u_err_bad_csr {
	NICIA_U_ERR_BAD_CSR_PID_ANY = 0x0,
};

struct nicia_u_err_cq_err {
	uint64_t cq_nbr;
} __attribute__((packed));

struct nicia_u_err_bad_csr_pid_any_info {
	struct nicia_u_err_cq_err cq_err;
} __attribute__((packed));

union nicia_u_err_bad_csr_info {
	struct nicia_u_err_bad_csr_pid_any_info pid_any;
};

enum nicia_u_err_bad_cmd {
	NICIA_U_ERR_BAD_CMD_CODE = 0x0,
	NICIA_U_ERR_BAD_CMD_LAST = 0x1,
	NICIA_U_ERR_BAD_CMD_SLOTS = 0x2,
	NICIA_U_ERR_BAD_CMD_HOST_CQ = 0x3,
	NICIA_U_ERR_BAD_CMD_SVC_TRIG = 0x4,
};

struct nicia_u_err_bad_cmd_code_info {
	struct nicia_u_err_cq_err cq_err;
} __attribute__((packed));

struct nicia_u_err_bad_cmd_last_info {
	struct nicia_u_err_cq_err cq_err;
} __attribute__((packed));

struct nicia_u_err_bad_cmd_slots_info {
	struct nicia_u_err_cq_err cq_err;
} __attribute__((packed));

struct nicia_u_err_bad_cmd_host_cq_info {
	struct nicia_u_err_cq_err cq_err;
} __attribute__((packed));

struct nicia_u_err_bad_cmd_svc_trig_info {
	struct nicia_u_err_cq_err cq_err;
} __attribute__((packed));

union nicia_u_err_bad_cmd_info {
	struct nicia_u_err_cq_err cq_err;

	struct nicia_u_err_bad_cmd_code_info code;
	struct nicia_u_err_bad_cmd_last_info last;
	struct nicia_u_err_bad_cmd_slots_info slots;
	struct nicia_u_err_bad_cmd_host_cq_info host_cq;
	struct nicia_u_err_bad_cmd_svc_trig_info svc_trig;
};

enum nicia_u_err_bad_ni {
	NICIA_U_ERR_BAD_NI_HPS_ERR = 0x0,
	NICIA_U_ERR_BAD_NI_INR = 0x1,
	NICIA_U_ERR_BAD_NI_TGT = 0x2,
	NICIA_U_ERR_BAD_NI_INVALID_MATCHING = 0x3,
	NICIA_U_ERR_BAD_NI_EQ = 0x4,
	NICIA_U_ERR_BAD_NI_CT_TRIG = 0x5,
	NICIA_U_ERR_BAD_NI_STATUS_REG = 0x6,
};

struct nicia_u_err_bad_ni_hps_err_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
} __attribute__((packed));

struct nicia_u_err_bad_ni_inr_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t md_handle : 25;
} __attribute__((packed));

struct nicia_u_err_bad_ni_tgt_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t pte_handle : 21;
} __attribute__((packed));

struct nicia_u_err_bad_ni_invalid_matching_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t me_handle : 25;
} __attribute__((packed));

struct nicia_u_err_bad_ni_eq_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t eq_handle : 22;
} __attribute__((packed));

struct nicia_u_err_bad_ni_ct_trig_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t ct_handle : 22;
} __attribute__((packed));

struct nicia_u_err_bad_ni_status_reg_info {
	uint64_t ni_opt : 2;
	/* FIELDS TBD */
} __attribute__((packed));

union nicia_u_err_bad_ni_info {
	struct nicia_u_err_bad_ni_hps_err_info hps_err;
	struct nicia_u_err_bad_ni_inr_info inr;
	struct nicia_u_err_bad_ni_tgt_info tgt;
	struct nicia_u_err_bad_ni_invalid_matching_info invalid_matching;
	struct nicia_u_err_bad_ni_eq_info eq;
	struct nicia_u_err_bad_ni_ct_trig_info ct_trig;
	struct nicia_u_err_bad_ni_status_reg_info status_reg;
};

enum nicia_u_err_bad_struct {
	NICIA_U_ERR_BAD_STRUCT_MD_RANGE = 0x0,
	NICIA_U_ERR_BAD_STRUCT_MD_VALID = 0x1,
	NICIA_U_ERR_BAD_STRUCT_PTE_RANGE = 0x2,
	NICIA_U_ERR_BAD_STRUCT_PTE_VALID = 0x3,
	NICIA_U_ERR_BAD_STRUCT_ENTRY_RANGE = 0x4,
	NICIA_U_ERR_BAD_STRUCT_ENTRY_VALID = 0x5,
	NICIA_U_ERR_BAD_STRUCT_ENTRY_OPTS = 0x6,
	NICIA_U_ERR_BAD_STRUCT_EQ_RANGE = 0x7,
	NICIA_U_ERR_BAD_STRUCT_EQ_VALID = 0x8,
	NICIA_U_ERR_BAD_STRUCT_CT_TRIG_RANGE = 0x9,
	NICIA_U_ERR_BAD_STRUCT_CT_VALID = 0xA,
	NICIA_U_ERR_BAD_STRUCT_EV_EQ_INVALID = 0xB,
	NICIA_U_ERR_BAD_STRUCT_EQ_SIZE = 0xC,
	NICIA_U_ERR_BAD_STRUCT_FREE_STRIDE = 0xD,
	NICIA_U_ERR_BAD_STRUCT_RELEASE_IN_USE = 0xE,
};

struct nicia_u_err_bad_struct_md_range_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t md_handle : 25;
} __attribute__((packed));

struct nicia_u_err_bad_struct_md_valid_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t md_handle : 25;
} __attribute__((packed));

struct nicia_u_err_bad_struct_pte_range_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t pte_handle : 21;
} __attribute__((packed));

struct nicia_u_err_bad_struct_pte_valid_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t pte_handle : 21;
} __attribute__((packed));

struct nicia_u_err_bad_struct_entry_range_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t pte_handle : 21;
	uint64_t me_handle : 25;
} __attribute__((packed));

struct nicia_u_err_bad_struct_entry_valid_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t pte_handle : 21;
	uint64_t me_handle : 25;
} __attribute__((packed));

struct nicia_u_err_bad_struct_entry_opts_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t pte_handle : 21;
	uint64_t me_handle : 25;
} __attribute__((packed));

struct nicia_u_err_bad_struct_eq_range_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t eq_handle : 22;
} __attribute__((packed));

struct nicia_u_err_bad_struct_eq_valid_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t eq_handle : 22;
} __attribute__((packed));

struct nicia_u_err_bad_struct_ct_trig_range_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t ct_handle : 22;
} __attribute__((packed));

struct nicia_u_err_bad_struct_ct_valid_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t ct_handle : 22;
} __attribute__((packed));

struct nicia_u_err_bad_struct_ev_eq_invalid_info {
	uint64_t ni_opt : 2;
	uint64_t eq_handle : 22;
} __attribute__((packed));

struct nicia_u_err_bad_struct_eq_size_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t eq_handle : 22;
	uint64_t size : 22;
} __attribute__((packed));

struct nicia_u_err_bad_struct_release_in_use_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t handle : 25;
} __attribute__((packed));

struct nicia_u_err_bad_struct_free_stride {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t handle : 22;
} __attribute__((packed));

union nicia_u_err_bad_struct_info {
	struct nicia_u_err_bad_struct_md_range_info md_range;
	struct nicia_u_err_bad_struct_md_valid_info md_valid;
	struct nicia_u_err_bad_struct_pte_range_info pte_range;
	struct nicia_u_err_bad_struct_pte_valid_info pte_valid;
	struct nicia_u_err_bad_struct_entry_range_info entry_range;
	struct nicia_u_err_bad_struct_entry_valid_info entry_valid;
	struct nicia_u_err_bad_struct_entry_opts_info entry_opts;
	struct nicia_u_err_bad_struct_eq_range_info eq_range;
	struct nicia_u_err_bad_struct_eq_valid_info eq_valid;
	struct nicia_u_err_bad_struct_ct_trig_range_info ct_trig_range;
	struct nicia_u_err_bad_struct_ct_valid_info ct_valid;
	struct nicia_u_err_bad_struct_ev_eq_invalid_info ev_eq_invalid;
	struct nicia_u_err_bad_struct_eq_size_info eq_size;
	struct nicia_u_err_bad_struct_free_stride free_stride;
	struct nicia_u_err_bad_struct_release_in_use_info release_in_use;
};

enum nicia_u_err_memory_access {
	NICIA_U_ERR_MEMORY_ACCESS_INR_BOUNDS = 0x0,
	NICIA_U_ERR_MEMORY_ACCESS_IPE_FAULT = 0x1,
	NICIA_U_ERR_MEMORY_ACCESS_INV_ATOMIC = 0x2,
	NICIA_U_ERR_MEMORY_ACCESS_REPLY_FAULT = 0x3,
	NICIA_U_ERR_MEMORY_ACCESS_TPE_FAULT = 0x4,
	NICIA_U_ERR_MEMORY_ACCESS_GET_FAULT = 0x5,
	NICIA_U_ERR_MEMORY_ACCESS_EPU_READ = 0x6,
	NICIA_U_ERR_MEMORY_ACCESS_EPU_WRITE = 0x7,
	NICIA_U_ERR_MEMORY_ACCESS_HOST_CQ_FAULT = 0x8,
	NICIA_U_ERR_MEMORY_ACCESS_REMOTE_TPE_FAULT = 0x9,
	NICIA_U_ERR_MEMORY_ACCESS_REMOTE_GET_TGT_FAULT = 0xA,
	NICIA_U_ERR_MEMORY_ACCESS_REMOTE_IPE_FAULT = 0xB,
	NICIA_U_ERR_MEMORY_ACCESS_REMOTE_GET_INR_FAULT = 0xC,
};

enum nicia_access_err_type {
	NICIA_ACCESS_ERR_TYPE_UNRESOLVED_PAGE_FAULT = 0b0,
	NICIA_ACCESS_ERR_TYPE_INVALID_PASID = 0b1,
	NICIA_ACCESS_ERR_TYPE_PCI_AT_REQ_FAILURE = 0b10,
};

struct nicia_u_err_memory_access_with_addr_info {
	uint64_t access_err_type : 2;
	uint64_t addr_56_12 : 45;
} __attribute__((packed));

struct nicia_u_err_memory_access_inr_cmd_err_info {
	uint64_t ni_opt : 2;
	uint64_t cmd_code : 6;
	uint64_t md_handle : 25;
} __attribute__((packed));

struct nicia_u_err_memory_access_inr_bounds_info {
	struct nicia_u_err_memory_access_inr_cmd_err_info inr_cmd_err;
} __attribute__((packed));

struct nicia_u_err_memory_access_ipe_fault_info {
	struct nicia_u_err_memory_access_with_addr_info with_addr;
} __attribute__((packed));

struct nicia_u_err_memory_access_inv_atomic_info {
	struct nicia_u_err_memory_access_inr_cmd_err_info inr_cmd_err;
} __attribute__((packed));

struct nicia_u_err_memory_access_reply_fault_info {
	struct nicia_u_err_memory_access_with_addr_info with_addr;
} __attribute__((packed));

struct nicia_u_err_memory_access_tpe_fault_info {
	struct nicia_u_err_memory_access_with_addr_info with_addr;
} __attribute__((packed));

struct nicia_u_err_memory_access_get_fault_info {
	struct nicia_u_err_memory_access_with_addr_info with_addr;
} __attribute__((packed));

struct nicia_u_err_memory_access_epu_read_info {
	struct nicia_u_err_memory_access_with_addr_info with_addr;
} __attribute__((packed));

struct nicia_u_err_memory_access_epu_write_info {
	struct nicia_u_err_memory_access_with_addr_info with_addr;
} __attribute__((packed));

struct nicia_u_err_memory_access_host_cq_fault_info {
	struct nicia_u_err_memory_access_with_addr_info with_addr;
} __attribute__((packed));

struct nicia_u_err_memory_access_remote_tpe_fault {
	struct nicia_u_err_process remote;
} __attribute__((packed));

struct nicia_u_err_memory_access_remote_get_tgt_fault {
	struct nicia_u_err_process remote;
} __attribute__((packed));

struct nicia_u_err_memory_access_remote_ipe_fault {
	struct nicia_u_err_process remote;
} __attribute__((packed));

struct nicia_u_err_memory_access_remote_get_inr_fault {
	struct nicia_u_err_process remote;
} __attribute__((packed));

union nicia_u_err_memory_access_info {
	struct nicia_u_err_memory_access_with_addr_info with_addr;
	struct nicia_u_err_memory_access_inr_cmd_err_info inr_cmd_err;
	struct nicia_u_err_process remote;

	struct nicia_u_err_memory_access_inr_bounds_info inr_bounds;
	struct nicia_u_err_memory_access_ipe_fault_info ipe_fault;
	struct nicia_u_err_memory_access_inv_atomic_info inv_atomic;
	struct nicia_u_err_memory_access_reply_fault_info reply_fault;
	struct nicia_u_err_memory_access_tpe_fault_info tpe_fault;
	struct nicia_u_err_memory_access_get_fault_info get_fault;
	struct nicia_u_err_memory_access_epu_read_info epu_read;
	struct nicia_u_err_memory_access_epu_write_info epu_write;
	struct nicia_u_err_memory_access_host_cq_fault_info host_cq_fault;
};

enum nicia_u_err_transfer {
	NICIA_U_ERR_TRANSFER_HEARTBEAT = 0x0,
	NICIA_U_ERR_TRANSFER_REQ = 0x1,
	NICIA_U_ERR_TRANSFER_WR_COMP = 0x2,
	NICIA_U_ERR_TRANSFER_PUT_DATA = 0x3,
	NICIA_U_ERR_TRANSFER_GET_DATA = 0x4,
	NICIA_U_ERR_TRANSFER_LINK_DOWN = 0x5,
};

struct nicia_u_err_transfer_heartbeat_info {
	struct nicia_u_err_node node;
} __attribute__((packed));

struct nicia_u_err_transfer_req_info {
	struct nicia_u_err_node node;
} __attribute__((packed));

struct nicia_u_err_transfer_wr_comp_info {
	struct nicia_u_err_node node;
} __attribute__((packed));

struct nicia_u_err_transfer_put_data_info {
	struct nicia_u_err_process process;
} __attribute__((packed));

struct nicia_u_err_transfer_get_data_info {
	struct nicia_u_err_process process;
} __attribute__((packed));

struct nicia_u_err_transfer_link_down_info {
	uint64_t port : 1; /* 0: input, 1: output */
	uint64_t f_tile : 1; /* 0: primary, 1: secondary */
} __attribute__((packed));

union nicia_u_err_transfer_info {
	struct nicia_u_err_node node;
	struct nicia_u_err_process process;

	struct nicia_u_err_transfer_heartbeat_info heartbeat;
	struct nicia_u_err_transfer_req_info req;
	struct nicia_u_err_transfer_wr_comp_info wr_comp;
	struct nicia_u_err_transfer_put_data_info put_data;
	struct nicia_u_err_transfer_get_data_info get_data;
	struct nicia_u_err_transfer_link_down_info link_down;
};

enum nicia_u_err_list_corruption {
	NICIA_U_ERR_LIST_CORRUPTION_UNLINK = 0x0,
	NICIA_U_ERR_LIST_CORRUPTION_APPEND_UH = 0x1,
	NICIA_U_ERR_LIST_CORRUPTION_SEARCH_UH = 0x2,
	NICIA_U_ERR_LIST_CORRUPTION_MATCHING = 0x3,
	NICIA_U_ERR_LIST_CORRUPTION_CT = 0x4,
	NICIA_U_ERR_LIST_CORRUPTION_TRIG = 0x5,
};

struct nicia_u_err_list_corruption_unlink_info {
	uint64_t pid : 10;
	uint64_t ni_opt : 2;
	uint64_t pte_handle : 21;
} __attribute__((packed));

struct nicia_u_err_list_corruption_append_uh_info {
	uint64_t pid : 10;
	uint64_t ni_opt : 2;
	uint64_t pte_handle : 21;
	uint64_t me_handle : 25;
} __attribute__((packed));

struct nicia_u_err_list_corruption_search_uh_info {
	uint64_t pid : 10;
	uint64_t ni_opt : 2;
	uint64_t pte_handle : 21;
} __attribute__((packed));

struct nicia_u_err_list_corruption_matching_info {
	uint64_t target_pid : 10;
	uint64_t target_pte_index : 21;
} __attribute__((packed));

struct nicia_u_err_list_corruption_ct_info {
	uint64_t pid : 10;
	uint64_t ni_opt : 2;
	uint64_t ct_handle : 22;
} __attribute__((packed));

struct nicia_u_err_list_corruption_trig_info {
	uint64_t pid : 10;
	uint64_t ni_opt : 2;
	uint64_t ct_handle : 22;
} __attribute__((packed));

union nicia_u_err_list_corruption_info {
	struct nicia_u_err_list_corruption_unlink_info unlink;
	struct nicia_u_err_list_corruption_append_uh_info append_uh;
	struct nicia_u_err_list_corruption_search_uh_info search_uh;
	struct nicia_u_err_list_corruption_matching_info matching;
	struct nicia_u_err_list_corruption_ct_info ct;
	struct nicia_u_err_list_corruption_trig_info trig;
};

enum nicia_u_err_tcpip {
	/* Three consecutive slots with last == 0 */
	NICIA_U_ERR_TCPIP_CMDS = 0x0,
	NICIA_U_ERR_TCPIP_SIZES = 0x1,
	NICIA_U_ERR_TCPIP_IOVEC = 0x2,
	NICIA_U_ERR_TCPIP_DMA_READ = 0x3,
	NICIA_U_ERR_TCPIP_GSO_SEG_SIZE = 0x4,
	NICIA_U_ERR_TCPIP_PKT_TOO_LARGE = 0x5,
};

struct nicia_u_err_tcpip_cmds_info {
	uint64_t cq_nbr : 6;
} __attribute__((packed));

struct nicia_u_err_tcpip_sizes_info {
	uint64_t cq_nbr : 6;
} __attribute__((packed));

struct nicia_u_err_tcpip_iovec_info {
	uint64_t cq_nbr : 6;
} __attribute__((packed));

struct nicia_u_err_tcpip_dma_read_info {
	uint64_t cq_nbr : 6;
	uint64_t addr : 56;
	uint64_t err_type : 2;
} __attribute__((packed));

struct nicia_u_err_tcpip_gso_seg_size_info {
	uint64_t cq_nbr : 6;
	uint64_t gso_seg_size : 16;
} __attribute__((packed));

struct nicia_u_err_tcpip_pkt_too_large_info {
	uint64_t cq_nbr : 6;
	uint64_t total_length : 16;
} __attribute__((packed));

union nicia_u_err_tcpip_info {
	struct nicia_u_err_tcpip_cmds_info cmds;
	struct nicia_u_err_tcpip_sizes_info sizes;
	struct nicia_u_err_tcpip_iovec_info iovec;
	struct nicia_u_err_tcpip_dma_read_info dma;
	struct nicia_u_err_tcpip_gso_seg_size_info gso;
	struct nicia_u_err_tcpip_pkt_too_large_info pkt;
};

union nicia_u_err_info {
	union nicia_u_err_bad_csr_info bad_csr;
	union nicia_u_err_bad_cmd_info bad_cmd;
	union nicia_u_err_bad_ni_info bad_ni;
	union nicia_u_err_bad_struct_info bad_struct;
	union nicia_u_err_memory_access_info memory_access;
	union nicia_u_err_transfer_info transfer;
	union nicia_u_err_list_corruption_info list_corruption;
	union nicia_u_err_tcpip_info tcpip;

	uint64_t value;
	struct {
		uint32_t value_lo;
		uint32_t value_hi;
	};
};

_Static_assert(sizeof(union nicia_u_err_info) == 8, "Error contexts must fit in 64bits");

/*
 * CSR containing the information for a portals uncorrectable error.
 */
union nicia_csr_err_u_log {
	struct {
		uint64_t valid : 1;
		uint64_t err_type : (4 - 1 + 1);
		uint64_t err_inst : (8 - 5 + 1);
		uint64_t err_info : (63 - 9 + 1);
	} __attribute__((packed)) fields;
	uint64_t csr;
	struct {
		uint32_t csr_lo;
		uint32_t csr_hi;
	};
};

_Static_assert(sizeof(union nicia_csr_err_u_log) == 8, "union nicia_csr_err_u_log must be 8 bytes");

/*
 * CSR containing the definition of a TCAM entry.
 */
union nicia_csr_pasid {
	struct {
		uint64_t valid : 1;
		uint64_t translate : 1;
		uint64_t s_path : 1;
		uint64_t pasid : 20;
		uint64_t pid : 10;
		uint64_t qemu_pgdir_50_20 : 31;
	} __attribute__((packed)) fields;
	uint64_t csr;
	struct {
		uint32_t csr_lo;
		uint32_t csr_hi;
	};
};

_Static_assert(sizeof(union nicia_csr_pasid) == 8, "union nicia_csr_pasid must be 8 bytes");

#endif /* BXI_INCLUDE_HW_H */
