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
 */

#pragma once

#ifdef __KERNEL__
#include <linux/types.h>
#else
#include <stdint.h>
#endif

#define NICIA_QEMU_CSR_VERSION 0

/* This range is not in use by the real HW */
#define NICIA_CSR_QEMU_OFFSET 0xFF00000
#define NICIA_CSR_QEMU_SIZE 0xFFFFF

/*
 * qemu_config
 * QEMU configuration options
 * RW - Cold reset
 */
union csr_qemu_config {
	struct {
		uint32_t hairpin : 1;
		uint32_t page_walk : 1;
		uint32_t reserved0 : 30;
	} __attribute__((packed)) fields;
	uint32_t csr;
};

#define NICIA_CSR_QEMU_CONFIG 0x20
#define NICIA_CSR_QEMU_CONFIG_WIDTH 32
#define NICIA_CSR_QEMU_CONFIG_MASK 0xffffffff
#define NICIA_CSR_QEMU_CONFIG_R
#define NICIA_CSR_QEMU_CONFIG_W
#define NICIA_CSR_QEMU_CONFIG_REPEAT 0x1
#define NICIA_CSR_QEMU_CONFIG_INCR 0x4

/*
 * qemu_state_reset - CSR
 * Reset the state to zero
 * W - N/A
 */
union csr_qemu_state_reset {
	struct {
		uint32_t reset : 1;
		uint32_t : 31;
	} __attribute__((packed)) fields;
	uint32_t csr;
};

_Static_assert(sizeof(union csr_qemu_state_reset) == 4,
	       "union csr_qemu_state_reset must be 4 bytes");

#define NICIA_CSR_QEMU_STATE_RESET 0x9C
#define NICIA_CSR_QEMU_STATE_RESET_WIDTH 1
#define NICIA_CSR_QEMU_STATE_RESET_MASK 0x1
#define NICIA_CSR_QEMU_STATE_RESET_W
#define NICIA_CSR_QEMU_STATE_RESET_REPEAT 0x1
#define NICIA_CSR_QEMU_STATE_RESET_INCR 0x4

/*
 * QEMU Interrupt
 */

/*
 * CSR to trigger a fatal error
 *
 * This csr must match the shape of `nicia_csr_emm_err_f_log`
 */
union csr_qemu_err_f_trigger {
	struct {
		uint32_t : 1;
		uint32_t err_type : 3;
		uint32_t err_info : (31 - 4 + 1);
	} __attribute__((packed)) fields;
	uint32_t csr;
};

#define NICIA_CSR_QEMU_ERR_F_TRIGGER 0x1d04
#define NICIA_CSR_QEMU_ERR_F_TRIGGER_WIDTH 32
#define NICIA_CSR_QEMU_ERR_F_TRIGGER_MASK 0xffffffff
#define NICIA_CSR_QEMU_ERR_F_TRIGGER_W
#define NICIA_CSR_QEMU_ERR_F_TRIGGER_REPEAT 0x1
#define NICIA_CSR_QEMU_ERR_F_TRIGGER_INCR 0x4

/*
 * V2P
 */

#define NICIA_CSR_QEMU_V2P_INVALIDATION_LO 0x2000
#define NICIA_CSR_QEMU_V2P_INVALIDATION_LO_WIDTH 32
#define NICIA_CSR_QEMU_V2P_INVALIDATION_LO_MASK 0xffffffff
#define NICIA_CSR_QEMU_V2P_INVALIDATION_LO_W
#define NICIA_CSR_QEMU_V2P_INVALIDATION_LO_REPEAT 128
#define NICIA_CSR_QEMU_V2P_INVALIDATION_LO_INCR 0x8

#define NICIA_CSR_QEMU_V2P_INVALIDATION_HI 0x2004
#define NICIA_CSR_QEMU_V2P_INVALIDATION_HI_WIDTH 32
#define NICIA_CSR_QEMU_V2P_INVALIDATION_HI_MASK 0xffffffff
#define NICIA_CSR_QEMU_V2P_INVALIDATION_HI_W
#define NICIA_CSR_QEMU_V2P_INVALIDATION_HI_REPEAT 128
#define NICIA_CSR_QEMU_V2P_INVALIDATION_HI_INCR 0x8

#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_COMPLETION 0x2500
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_COMPLETION_WIDTH 32
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_COMPLETION_MASK 0xffffffff
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_COMPLETION_W
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_COMPLETION_REPEAT 0x1
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_COMPLETION_INCR 0x4

#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_LO 0x2504
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_LO_WIDTH 32
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_LO_MASK 0xffffffff
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_LO_R
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_LO_W
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_LO_REPEAT 1
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_LO_INCR 0x4

#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_HI 0x2508
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_HI_WIDTH 32
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_HI_MASK 0xffffffff
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_HI_R
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_HI_W
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_HI_REPEAT 1
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_RING_ADDR_HI_INCR 0x4

#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_HEAD_ACK 0x250c
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_HEAD_ACK_WIDTH 32
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_HEAD_ACK_MASK 0xffffffff
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_HEAD_ACK_R
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_HEAD_ACK_W
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_HEAD_ACK_REPEAT 1
#define NICIA_CSR_QEMU_V2P_PAGE_REQUEST_HEAD_ACK_INCR 0x4

#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_LO 0x2510
#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_LO_WIDTH 32
#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_LO_MASK 0xffffffff
#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_LO_R
#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_LO_W
#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_LO_REPEAT 1
#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_LO_INCR 0x4

#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_HI 0x2514
#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_HI_WIDTH 32
#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_HI_MASK 0xffffffff
#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_HI_R
#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_HI_W
#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_HI_REPEAT 1
#define NICIA_CSR_QEMU_V2P_INV_COMPLETION_ADDR_HI_INCR 0x4

#define NICIA_QEMU_V2P_PAGE_REQUEST_IRQ 510
