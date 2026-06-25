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

#define BXI_PIO_MAX_PAYLOAD_SIZE 480

#define BXI_CLUSTER_ID_MAX ((1u << 7) - 1)
/* HW NID 0xffff is forbidden */
#define BXI_HW_NID_MAX (((1u << 16) - 1) - 1)
#define BXI_VFID_MAX ((1u << 7) - 1)

#define BXI3_HW_CAP_QEMU (1 << 2)

/*
 * Legacy absent capabilities (mapping to position 0)
 */
#define BXI_HW_CAP_FPGA (1 << 0)
#define BXI_HW_CAP_PUT_RDVPUT_MATCHING (1 << 0)

/*
 * Legacy always present capabilities (mapping to position 1)
 */
#define BXI_HW_CAP_IOVEC_OFFSET (1 << 1)
#define BXI_HW_CAP_SWAP (1 << 1)
#define BXI_HW_CAP_SEARCH_COMPLETED (1 << 1)
#define BXI_HW_CAP_TRIGGERED (1 << 1)
#define BXI_HW_CAP_FC_EQFULL_SERVICE (1 << 1)
#define BXI_HW_CAP_FC_EQFULL_COMPUTE (1 << 1)
#define BXI_HW_CAP_EV_SEND_FIXED (1 << 1)
#define BXI_HW_CAP_MANAGE_LOCAL_OPTS (1 << 1)
