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
#include <asm/signal.h>
#define __SIGRTMIN SIGRTMIN
#else
#include <signal.h>
#endif

/* Skip real time signals used by glibc POSIX thread implementation */
#define SIGBXI3 (__SIGRTMIN + 10)

#define BXI3_PING_SRV_PID 0
#define BXI3_PING_SRV_SVC_PT 0
#define BXI3_PING_SRV_CPT_PT 1

#define BXI3_MIN_EQ_SIZE 7
#define BXI3_MAX_EQ_SIZE ((1 << 21) - 1)

#define PTLPING_DEFAULT_MSGSIZE 0

/* Number of lower PID reserved for service. */
#define BXI3_NBR_OF_RESERVED_PID 10
#define BXI3_MIN_COMPUTE_PID BXI3_NBR_OF_RESERVED_PID
