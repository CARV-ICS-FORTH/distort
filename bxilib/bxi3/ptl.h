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

#pragma once

#define PTL_UID_ANY 0xffffffff

#define PTL_PID_MAX 1022
#define PTL_PID_COUNT (PTL_PID_MAX + 1)
#define PTL_PID_ANY 1023

/*
 * Return codes
 *
 * Values over 0x100 are software only.
 *
 * HAS 0.83: Table 62, section 4.4.3.4
 */
#define PTL_OK 0x0
#define PTL_NO_INIT 0x1
#define PTL_ARG_INVALID 0x2
#define PTL_NO_SPACE 0x3
#define PTL_IN_USE 0x4
#define PTL_LIST_TOO_LONG 0x5
#define PTL_FAIL 0xD
#define PTL_PT_IN_USE 0xE

#define PTL_ABORTED 0x100
#define PTL_CT_NONE_REACHED 0x101
#define PTL_EQ_DROPPED 0x102
#define PTL_EQ_EMPTY 0x103
#define PTL_IGNORED 0x104
#define PTL_PID_IN_USE 0x105
#define PTL_PT_EQ_NEEDED 0x106
#define PTL_PT_FULL 0x107

/*
 * NI Options
 *
 * HAS: Table 2, section 4.1
 */

#define PTL_NI_LOGICAL (0 << 0)
#define PTL_NI_MATCHING (1 << 1)
#define PTL_NI_NO_MATCHING (0 << 1)
#define PTL_NI_PHYSICAL (1 << 0)

/*
 * ME/LE Options & Type
 *
 * HAS: Table 50, section 4.4.3.2
 */
#define PTL_ME_OP_PUT (1 << 0)
#define PTL_ME_OP_GET (1 << 1)
#define PTL_ME_MANAGE_LOCAL (1 << 2)
#define PTL_ME_LOCAL_INC_UH_RLENGTH (1 << 3)
#define PTL_ME_NO_TRUNCATE (1 << 4)
#define PTL_ME_USE_ONCE (1 << 5)
#define PTL_ME_UNEXPECTED_HDR_DISABLE (1 << 6)
#define PTL_ME_EVENT_LINK_DISABLE (1 << 8)
#define PTL_ME_EVENT_FLOWCTRL_DISABLE (1 << 9)
#define PTL_ME_EVENT_UNLINK_DISABLE (1 << 10)
#define PTL_ME_EVENT_COMM_DISABLE (1 << 11)
#define PTL_ME_EVENT_SUCCESS_DISABLE (1 << 12)
#define PTL_ME_EVENT_OVER_DISABLE (1 << 13)
#define PTL_ME_EVENT_CT_COMM (1 << 14)
#define PTL_ME_EVENT_CT_OVERFLOW (1 << 15)
#define PTL_ME_EVENT_CT_BYTES (1 << 16)
#define PTL_ME_MAY_ALIGN (1 << 17)
#define PTL_ME_IS_ACCESSIBLE (1 << 18)
#define PTL_BXI3_REPLY_WR_IS_OBSERVABLE (1 << 19)

#define PTL_LE_OP_PUT PTL_ME_OP_PUT
#define PTL_LE_OP_GET PTL_ME_OP_GET
#define PTL_LE_MANAGE_LOCAL PTL_ME_MANAGE_LOCAL
#define PTL_LE_USE_ONCE PTL_ME_USE_ONCE
#define PTL_LE_UNEXPECTED_HDR_DISABLE PTL_ME_UNEXPECTED_HDR_DISABLE
#define PTL_LE_EVENT_LINK_DISABLE PTL_ME_EVENT_LINK_DISABLE
#define PTL_LE_EVENT_FLOWCTRL_DISABLE PTL_ME_EVENT_FLOWCTRL_DISABLE
#define PTL_LE_EVENT_UNLINK_DISABLE PTL_ME_EVENT_UNLINK_DISABLE
#define PTL_LE_EVENT_COMM_DISABLE PTL_ME_EVENT_COMM_DISABLE
#define PTL_LE_EVENT_SUCCESS_DISABLE PTL_ME_EVENT_SUCCESS_DISABLE
#define PTL_LE_EVENT_OVER_DISABLE PTL_ME_EVENT_OVER_DISABLE
#define PTL_LE_EVENT_CT_COMM PTL_ME_EVENT_CT_COMM
#define PTL_LE_EVENT_CT_OVERFLOW PTL_ME_EVENT_CT_OVERFLOW
#define PTL_LE_EVENT_CT_BYTES PTL_ME_EVENT_CT_BYTES
#define PTL_LE_MAY_ALIGN PTL_ME_MAY_ALIGN
#define PTL_LE_IS_ACCESSIBLE PTL_ME_IS_ACCESSIBLE

/*
 * ME/LE structures
 *
 * HAS: Table 4, section 4.2.7
 */

typedef enum {
	PTL_PRIORITY_LIST = 0,
	PTL_OVERFLOW_LIST = 1,
} ptl_list_t;

/*
 * Event structures
 *
 * HAS: Table 82, section 4.5.3
 */
typedef enum {
	PTL_EVENT_GET = 0x0,
	PTL_EVENT_GET_OVERFLOW = 0x1,
	PTL_EVENT_PUT = 0x2,
	PTL_EVENT_PUT_OVERFLOW = 0x3,
	PTL_EVENT_ATOMIC = 0x4,
	PTL_EVENT_ATOMIC_OVERFLOW = 0x5,
	PTL_EVENT_FETCH_ATOMIC = 0x6,
	PTL_EVENT_FETCH_ATOMIC_OVERFLOW = 0x7,
	PTL_EVENT_REPLY = 0x8,
	PTL_EVENT_SEND = 0x9,
	PTL_EVENT_ACK = 0xA,
	PTL_EVENT_PT_DISABLED = 0xB,
	PTL_EVENT_AUTO_UNLINK = 0xC,
	PTL_EVENT_AUTO_FREE = 0xD,
	PTL_EVENT_SEARCH = 0xE,
	PTL_EVENT_LINK = 0xF,
	PTL_EVENT_ERROR = 0x10,
} ptl_event_kind_t;

/* HAS: Table 59, section 4.4.3.4 */
typedef enum {
	PTL_NI_OK = PTL_OK,
	PTL_NI_UNDELIVERABLE = 0x6,
	PTL_NI_PT_DISABLED = 0x7,
	PTL_NI_DROPPED = 0x8,
	PTL_NI_PERM_VIOLATION = 0x9,
	PTL_NI_OP_VIOLATION = 0xA,
	PTL_NI_SEGV = 0xB,
	PTL_NI_NO_MATCH = 0xC,

	/* BXI Extensions */
	PTL_NI_SEGV_REMOTE = 0xF,
	PTL_NI_ARG_INVALID = PTL_ARG_INVALID,
} ptl_ni_fail_t;

enum ptl_cq_mode_t {
	PTL_BXI3_NIC_CQ = 0,
	PTL_BXI3_HOST_CQ = 1,
	PTL_BXI3_HYBRID_HOST_NIC_CQ,
};
typedef enum ptl_cq_mode_t ptl_cq_mode_t;

/* Host CQ constant */
#define BXI_HOST_CQ_THRESHOLD 4
#define BXI_HOST_CQ_MIN_THRESHOLD 4
_Static_assert(BXI_HOST_CQ_THRESHOLD >= BXI_HOST_CQ_MIN_THRESHOLD, "Invalid host CQ threshold");
