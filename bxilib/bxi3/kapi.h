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

#include <linux/types.h>
#include <linux/cpumask.h>

#define BXI3_KAPI_VERSION 0

/* Private context of the bxi3 driver */
struct bxi3_kapi_context;

/*
 * This struct is versioned, so that changes to the struct will still be compatible with
 * newer portals driver. The bxi3 driver will only fill fields relevant to the version specified
 * in the bxi3_kapi_open call
 *
 * This also means that fields must only be added at the end of the struct, not re-ordered
 */
struct bxi3_kapi_info {
	/* Version 0 */
	u16 pid;
	u32 cluster_id;
	u32 hw_nid;
	u32 vfid;
	u32 ptl_nid;
	u64 capabilities;
	u16 put_rma_threshold;
	u16 max_data_pkt_size;
};

/*
 * This struct, similarly to the info struct, is versioned for compatibility.
 * Missing fields due to compatibility will be read as being some default value
 */
struct bxi3_kapi_init {
	/* Version 0 */
	u16 nbr_of_cqs;
	u16 desired_pid;
};

struct bxi3_kapi_context *bxi3_kapi_init(int version, int interface,
					 const struct bxi3_kapi_init *init,
					 struct bxi3_kapi_info *info);

int bxi3_kapi_fini(struct bxi3_kapi_context *ctx);

/*
 * Invoke a callback on each CQ allocated to the process.
 * The callback is passed:
 *  - the index of the cq in the linked list
 *  - a pointer to the PCI region containing the slots
 *  - a pointer to the PCI region containing the CQ CSRs
 *  - a private pointer for use by the callback
 */
typedef void (*for_each_cq_cb)(size_t index, void *slots, void __iomem *csr, void __iomem *secure,
			       void *private);
void bxi3_kapi_for_each_cq(struct bxi3_kapi_context *ctx, for_each_cq_cb callback, void *private);

/* Allocate a DMA coherent region associated with the BXI device */
void *bxi3_kapi_dma_alloc(struct bxi3_kapi_context *ctx, size_t size, dma_addr_t *dma_handle,
			  gfp_t gfp);

/* Free a DMA coherent region allocated with bxi3_kapi_dma_alloc */
void bxi3_kapi_dma_free(struct bxi3_kapi_context *ctx, size_t size, void *cpu_addr,
			dma_addr_t dma_addr);

/*
 * Users of the kernelspace portals library need to allocate/map
 * memory for DMA operations (dma_alloc_..., dma_map_...).
 * To do so, a pointer to the device is required.
 * This function can be used to get that pointer.
 */
struct device *bxi3_kapi_get_device(struct bxi3_kapi_context *ctx);

typedef void (*cq_irq_callback)(void *arg, u16 cq_index);
void bxi3_kapi_set_cq_irq_callback(struct bxi3_kapi_context *ctx, cq_irq_callback cb, void *arg);
int bxi3_kapi_cq_alloc_irq(struct bxi3_kapi_context *ctx, u16 cq_index, const cpumask_t *m);
void bxi3_kapi_cq_free_irq(struct bxi3_kapi_context *ctx, u16 cq_index);
