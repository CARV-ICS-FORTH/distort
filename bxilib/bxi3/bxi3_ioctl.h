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

#ifdef __KERNEL__
#include <linux/types.h>
#else
#include <stdint.h>
#include <stdbool.h>
#endif

#define BXI3_IOCTL_VER_MAJOR 1
#define BXI3_IOCTL_VER_MINOR 0

#define BXI3_IOCTL_MAGIC 'I'

struct bxi3_info {
	uint16_t pid;
	uint16_t put_rma_threshold;
	uint16_t max_data_pkt_size;
	uint64_t capabilities;
	unsigned int ptl_nid;
};

struct bxi3_hwinfo {
	uint64_t capabilities;
	unsigned int cluster_id;
	unsigned int hw_nid; /* Differs from Portals NID */
	unsigned int vfid;
};

struct bxi3_init_arg {
	bool service;
	uint16_t nbr_of_cqs;
	uint16_t pid_desired;
};

#define BXI3_IOCTL_GET_INFO _IOR(BXI3_IOCTL_MAGIC, 0x40, struct bxi3_info)
#define BXI3_IOCTL_GET_VERSION _IO(BXI3_IOCTL_MAGIC, 0x41)
#define BXI3_IOCTL_INIT _IOW(BXI3_IOCTL_MAGIC, 0x42, struct bxi3_init_arg)
#define BXI3_IOCTL_GET_HW_INFO _IOR(BXI3_IOCTL_MAGIC, 0x44, struct bxi3_hwinfo)
