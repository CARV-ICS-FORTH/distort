#ifndef _HW_COMMAND_H
#define _HW_COMMAND_H

#include "linux/bxi3/hw.h"

/*
 * Table 14 in NICIA HAS 0.87: Supported commands ("The external commands NI_INIT,
 * NI_FINI, ME_RECOVER are sent to HPS using the same format than the one
 * received from CQ")
 */
#define NICIA_NI_INIT_CMD 0x00
#define NICIA_NI_FINI_CMD 0x01
#define NICIA_PTE_INIT_CMD 0x02
#define NICIA_PTE_INV_CMD 0x03
#define NICIA_PTE_DISABLE_CMD 0x04
#define NICIA_PTE_ENABLE_CMD 0x05
#define NICIA_MD_INIT_CMD 0x06
#define NICIA_MD_INV_CMD 0x07
#define NICIA_LE_APPEND_CMD 0x08
#define NICIA_ME_APPEND_CMD 0x09
#define NICIA_UNLINK_CMD 0x0A
#define NICIA_SEARCH_CMD 0x0B
#define NICIA_EQ_INIT_CMD 0x0C
#define NICIA_EQ_INV_CMD 0x0D
#define NICIA_PUT_CMD 0x13
#define NICIA_GET_CMD 0x14
#define NICIA_VOID_CMD 0x21
#define NICIA_NI_STATUS_CMD 0x20

#define NICIA_PUT_PIO_SIZE 60

/*
 * For commands the HW imposes writing blocks of 64 bits
 * aligned on 64 bits. For responses the HW is able
 * to perform DMA write only on 64 bits alignment.
 * To do so, we use an attribute called aligned(8)
 */

/* Generic RAW command */
struct nicia_raw_ptl_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	/* unused: 505 */
	uint64_t : 57;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_raw_ptl_cmd) == 64, "Command must be 64 bytes");

struct nicia_raw_ptl_first_slot {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	/* unused: 486 */
	uint64_t : 38;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_raw_ptl_first_slot) == 64, "Command must be 64 bytes");

/* Generic RAW command response */
struct nicia_cmd_raw_resp {
	uint64_t valid : 1;
	uint64_t : 7;
	uint64_t cmd_code : 6;
	uint64_t : 2;
	uint64_t fail_type : 4;
	/* unused: 492 */
	uint64_t : 44;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_cmd_raw_resp) == 64, "Response must be 64 bytes");

/* Table 10 in NICIA HAS 0.95: NI_INIT command */
struct nicia_ni_init_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t ptl_rsrc_slice_nb : 10; /* HW filled */
	uint64_t : 27;
	uint64_t compute_line : 1;
	uint64_t uid : 32; /* HW filled */
	uint64_t partition_key : 12; /* HW filled */
	uint64_t : 20;
	uint64_t me_max : 25;
	uint64_t : 7;
	uint64_t uh_max : 25;
	uint64_t : 7;
	uint64_t md_max : 25;
	uint64_t : 7;
	uint64_t eq_max : 22;
	uint64_t : 10;
	uint64_t ct_max : 22;
	uint64_t : 10;
	uint64_t pte_max : 21;
	uint64_t : 11;
	uint64_t trig_max : 23;
	/* unused: 105 */
	uint64_t : 41;
	uint64_t : 64;
	uint64_t resp_addr : 57;
	uint64_t : 7;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_ni_init_cmd) == 64, "Command must be 64 bytes");

/* Table 12 in NICIA HAS 0.83: PTE_INIT command */
struct nicia_pte_init_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t : 6;
	uint64_t options : 4;
	uint64_t s_path : 1; /* HW filled */
	uint64_t : 27;
	uint64_t eq_handle : 22;
	uint64_t : 10;
	uint64_t pte_handle : 21;
	uint64_t : 11;
	uint64_t free_base_addr : 57;
	uint64_t : 7;
	uint64_t free_stride : 7;
	/* 249 */
	uint64_t : 57;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t resp_addr : 57;
	uint64_t : 7;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_pte_init_cmd) == 64, "Command must be 64 bytes");

/* Table 13 in NICIA HAS 0.83: PTE_INV command */
struct nicia_pte_inv_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t : 6;
	uint64_t pte_handle : 21;
	/* 395 */
	uint64_t : 11;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t resp_addr : 57;
	uint64_t : 7;
};

_Static_assert(sizeof(struct nicia_pte_inv_cmd) == 64, "Command must be 64 bytes");

/* Table 14 in NICIA HAS 0.83: PTE_DISABLE command */
struct nicia_pte_disable_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t : 6;
	uint64_t pte_handle : 21;
	/* unused: 395 */
	uint64_t : 11;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t resp_addr : 57;
	uint64_t : 7;
};

_Static_assert(sizeof(struct nicia_pte_disable_cmd) == 64, "Command must be 64 bytes");

/* Table 15 in NICIA HAS 0.83: PTE_ENABLE command */
struct nicia_pte_enable_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t : 6;
	uint64_t pte_handle : 21;
	/* unused: 395 */
	uint64_t : 11;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t resp_addr : 57;
	uint64_t : 7;
};

_Static_assert(sizeof(struct nicia_pte_enable_cmd) == 64, "Command must be 64 bytes");

/* Table 24 in NICIA HAS 0.87 LE_APPEND command */
struct nicia_le_append_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t list_type : 1;
	uint64_t options12_0 : 13;
	uint64_t : 3;
	uint64_t pte_handle : 21;
	uint64_t uid : 32; /* HW filled */
	uint64_t options19_13 : 7;
	uint64_t me_handle : 25;
	uint64_t iovec_size : 30;
	uint64_t : 12;
	uint64_t ct_handle : 22;
	uint64_t start : 57;
	uint64_t : 7;
	uint64_t length : 57;
	uint64_t : 7;
	uint64_t user_ptr : 64;
	uint64_t min_free : 57;
	/* unused: 71 */
	uint64_t : 7;
	uint64_t : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_le_append_cmd) == 64, "Command must be 64 bytes");

/* Table 25 in NICIA HAS 0.87 ME_APPEND command */
struct nicia_me_append_slot1_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t list_type : 1;
	uint64_t options12_0 : 13;
	uint64_t : 3;
	uint64_t pte_handle : 21;
	uint64_t uid : 32; /* HW filled */
	uint64_t options19_13 : 7;
	uint64_t me_handle : 25;
	uint64_t match_id : 40;
	uint64_t : 2;
	uint64_t ct_handle : 22;
	uint64_t start : 57;
	uint64_t : 7;
	uint64_t length : 57;
	uint64_t : 7;
	uint64_t user_ptr : 64;
	uint64_t match_bits : 64;
	uint64_t ignore_bits : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_me_append_slot1_cmd) == 64, "Command must be 64 bytes");

struct nicia_me_append_slot2_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t min_free : 57;
	uint64_t iovec_size : 30;
	uint64_t : 34;
	uint64_t size_threshold : 57;
	uint64_t match_less_thr : 1;
	/* unused: 326 */
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 6;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_me_append_slot2_cmd) == 64, "Command must be 64 bytes");

/* Table 26 in NICIA HAS 0.87: UNLINK command */
struct nicia_unlink_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t : 17;
	uint64_t pte_handle : 21;
	uint64_t : 39;
	uint64_t me_handle : 25;
	/* unused: 320 */
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t resp_addr : 57;
	uint64_t : 7;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_unlink_cmd) == 64, "Command must be 64 bytes");

/* Table 30 in NICIA HAS 0.97 SEARCH command */
struct nicia_search_slot1_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t search_option : 1;
	uint64_t options13_0 : 14;
	uint64_t : 2;
	uint64_t pte_handle : 21;
	uint64_t uid : 32; /* HW filled */
	uint64_t options20_14 : 7;
	uint64_t : 25;
	uint64_t match_id : 40;
	uint64_t : 2;
	uint64_t ct_handle : 22;
	uint64_t start : 57;
	uint64_t : 7;
	uint64_t length : 57;
	uint64_t : 7;
	uint64_t user_ptr : 64;
	uint64_t match_bits : 64;
	uint64_t ignore_bits : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_search_slot1_cmd) == 64, "Command must be 64 bytes");

struct nicia_search_slot2_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t min_free : 57;
	uint64_t iovec_size : 30;
	uint64_t : 34;
	uint64_t size_threshold : 36;
	uint64_t match_less_thr : 1;
	/* unused: 347 */
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 27;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_search_slot2_cmd) == 64, "Command must be 64 bytes");

/* Table 31 in NICIA HAS 0.95: EQ_INIT command */
struct nicia_eq_init_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t : 5;
	uint64_t interrupt_val : 1;
	uint64_t interrupt_nb : 10; /* HW filled */
	uint64_t eq_handle : 22;
	uint64_t size : 22;
	uint64_t : 42;
	uint64_t buffer_start : 57;
	uint64_t : 7;
	uint64_t head_addr : 57;
	/* unused: 199 */
	uint64_t : 7;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t resp_addr : 57;
	uint64_t : 7;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_eq_init_cmd) == 64, "Command must be 64 bytes");

/* Table 23 in NICIA HAS 0.83: EQ_INV command */
struct nicia_eq_inv_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t : 16;
	uint64_t eq_handle : 22;
	/* unused: 384 */
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t resp_addr : 57;
	uint64_t : 7;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_eq_inv_cmd) == 64, "Command must be 64 bytes");

/* Table 16 in NICIA HAS 0.83: MD_INIT command */
struct nicia_md_init_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t : 6;
	uint64_t md_handle : 25;
	uint64_t : 7;
	uint64_t start : 57;
	uint64_t : 7;
	uint64_t length : 57;
	uint64_t : 7;
	uint64_t iovec_size : 30;
	uint64_t : 2;
	uint64_t option : 10;
	uint64_t : 22;
	uint64_t ct_handle : 22;
	uint64_t : 10;
	uint64_t eq_handle : 22;
	/* unused: 138 */
	uint64_t : 10;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t resp_addr : 57;
	uint64_t : 7;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_md_init_cmd) == 64, "Command must be 64 bytes");

/* Table 17 in NICIA HAS 0.83: MD_INV command */
struct nicia_md_inv_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t : 6;
	uint64_t md_handle : 25;
	/* Unused 391 */
	uint64_t : 7;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t resp_addr : 57;
	uint64_t : 7;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_md_inv_cmd) == 64, "Command must be 64 bytes");

/* Table 32 in NICIA HAS 0.85: PUT-DMA command */
struct nicia_put_dma_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t adaptive : 1;
	uint64_t unreliable : 1;
	uint64_t length : 36;
	uint64_t pte_handle : 21;
	uint64_t pio : 1;
	uint64_t md_use_once : 1;
	uint64_t : 1;
	uint64_t destination : 40;
	uint64_t initiator_rank : 32;
	uint64_t ack_type : 2;
	uint64_t : 5;
	uint64_t put_md_handle : 25; /* Only used when md_use_once == 0 */
	uint64_t match_bits : 64;
	uint64_t remote_offset : 57;
	uint64_t hdr_data1_0 : 2;
	uint64_t : 5;
	uint64_t local_offset : 57; /* Only used when md_use_once == 0 */
	uint64_t : 1;
	uint64_t hdr_data7_2 : 6;
	uint64_t user_ptr : 64;
	uint64_t hdr_data71_8 : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_put_dma_cmd) == 64, "Command must be 64 bytes");

struct nicia_put_pio_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t : 25;
	uint8_t data[NICIA_PUT_PIO_SIZE];
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_put_pio_cmd) == 64, "Command must be 64 bytes");

struct nicia_comm_once_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t : 57;
	uint64_t start : 57;
	uint64_t : 7;
	uint64_t : 57;
	uint64_t : 7;
	/* Unused : 128 */
	uint64_t : 64;
	uint64_t : 64;
	uint64_t iovec_size : 30;
	uint64_t : 2;
	uint64_t options : 10;
	uint64_t : 22;
	uint64_t ct_ptr : 22;
	uint64_t : 10;
	uint64_t eq_ptr : 22;
	/* Unused : 74 */
	uint64_t : 10;
	uint64_t : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_comm_once_cmd) == 64, "Command must be 64 bytes");

struct nicia_get_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t adaptive : 1;
	uint64_t unreliable : 1;
	uint64_t length : 36;
	uint64_t pte_handle : 21;
	uint64_t : 1;
	uint64_t md_use_once : 1;
	uint64_t : 1;
	uint64_t destination : 40;
	uint64_t initiator_rank : 32;
	uint64_t : 7;
	uint64_t get_md_handle : 25;
	uint64_t match_bits : 64;
	uint64_t remote_offset : 57;
	uint64_t hdr_data1_0 : 2;
	uint64_t : 5;
	uint64_t local_offset : 57;
	uint64_t : 1;
	uint64_t hdr_data7_2 : 6;
	uint64_t user_ptr : 64;
	uint64_t hdr_data71_8 : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_get_cmd) == 64, "Command must be 64 bytes");

/* Table 54 in NICIA HAS 0.65: VOID_CMD command */
struct nicia_void_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	/* unused: 505 */
	uint64_t : 57;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_void_cmd) == 64, "Command must be 64 bytes");

/* Table 20 in HAS 0.96 */
struct nicia_ni_status_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	uint64_t : 6;
	uint64_t status_register : 2;
	/* unused: 414 */
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 30;
	uint64_t resp_addr : 57;
	uint64_t : 7;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_ni_status_cmd) == 64, "Command must be 64 bytes");

/* Table 89 in HAS 0.96 */
struct nicia_ni_status_resp {
	uint64_t valid : 1;
	uint64_t : 7;
	uint64_t cmd_code : 6;
	uint64_t : 2;
	uint64_t fail_type : 4;
	uint64_t : 44;
	uint64_t counter : 64;
	/* unused : 384 */
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_ni_status_resp) == 64, "Response must be 64 bytes");

/* Table 11 in NICIA HAS 0.83: NI_FINI raw command */
struct nicia_ni_fini_cmd {
	uint64_t last : 1;
	uint64_t cmd_code : 6;
	uint64_t pid : 10; /* HW filled */
	uint64_t vfid : 7; /* HW filled */
	uint64_t ni_opt : 2;
	/*
	 * Setting the 'all_ni' field to true allows to finalize in a
	 * single command all the NI initialized for a given pid/vfid.
	 * This field is only used by the software and does not appear in the NICIA specification.
	 */
	uint64_t all_ni : 1;
	/* unused: 421 */
	uint64_t : 63;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 38;
	uint64_t resp_addr : 57;
	uint64_t : 7;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_ni_fini_cmd) == 64, "Command must be 64 bytes");

/* Table 60 in NICIA HAS 0.83: NI_INIT raw command response to host */
struct nicia_ni_init_resp_cmd {
	uint64_t valid : 1;
	uint64_t : 7;
	uint64_t cmd_code : 6;
	uint64_t : 2;
	uint64_t fail_type : 4;
	uint64_t : 12;
	uint64_t max_le_me : 32;
	uint64_t max_uh : 32;
	uint64_t max_md : 32;
	uint64_t max_eq : 32;
	uint64_t max_ct : 32;
	uint64_t max_pte : 32;
	uint64_t max_triggered_op : 32;
	/* unused: 256 */
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_ni_init_resp_cmd) == 64, "Response must be 64 bytes");

/* Table 61 in NICIA HAS 0.83: Portals return code response
 * The response to NI_FINI command is a Portals return code written to host through EPU at
 * address resp_addr.
 */
struct nicia_portals_resp_cmd {
	uint64_t valid : 1;
	uint64_t : 7;
	uint64_t cmd_code : 6;
	uint64_t : 2;
	uint64_t fail_type : 4;
	/* unused: 492 */
	uint64_t : 44;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_portals_resp_cmd) == 64, "Response must be 64 bytes");

/* Table 116 in NICIA HAS 0.83: Format of the first slot of a TCP/IP TX command */
struct nicia_ntl_otx_slot1_cmd {
	uint64_t last : 1;
	uint64_t : 2;
	uint64_t iovec_size : 5;
	uint64_t offload_options : 8;
	uint64_t gso_seg_size : 16;
	uint64_t resp_addr : 58;
	uint64_t start0 : 57;
	uint64_t length0 : 16;
	uint64_t start1 : 57;
	uint64_t length1 : 16;
	uint64_t start2 : 57;
	uint64_t length2 : 16;
	uint64_t start3 : 57;
	uint64_t length3 : 16;
	uint64_t start4 : 57;
	uint64_t length4 : 16;
	uint64_t start5 : 57;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_ntl_otx_slot1_cmd) == 64, "Command must be 64 bytes");

/* Table 117 in NICIA HAS 0.83: Format of the second slot of a TCP/IP TX command */
struct nicia_ntl_otx_slot2_cmd {
	uint64_t last : 1;
	uint64_t length5 : 16;
	uint64_t start6 : 57;
	uint64_t length6 : 16;
	uint64_t start7 : 57;
	uint64_t length7 : 16;
	uint64_t start8 : 57;
	uint64_t length8 : 16;
	uint64_t start9 : 57;
	uint64_t length9 : 16;
	uint64_t start10 : 57;
	uint64_t length10 : 16;
	uint64_t start11 : 57;
	uint64_t length11 : 16;
	uint64_t start12 : 57;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_ntl_otx_slot2_cmd) == 64, "Command must be 64 bytes");

/* Table 118 in NICIA HAS 0.83: Format of the third slot of a TCP/IP TX command */
struct nicia_ntl_otx_slot3_cmd {
	uint64_t last : 1;
	uint64_t length12 : 16;
	uint64_t start13 : 57;
	uint64_t length13 : 16;
	uint64_t start14 : 57;
	uint64_t length14 : 16;
	uint64_t start15 : 57;
	uint64_t length15 : 16;
	uint64_t start16 : 57;
	uint64_t length16 : 16;
	uint64_t start17 : 57;
	uint64_t length17 : 16;
	/* unused: 130 */
	uint64_t : 2;
	uint64_t : 64;
	uint64_t : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_ntl_otx_slot3_cmd) == 64, "Command must be 64 bytes");

/* Table 120 in NICIA HAS 0.83: TCP/IP RX command format */
struct nicia_ntl_orx_cmd {
	uint64_t valid : 1;
	uint64_t : 6;
	uint64_t start : 57;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_ntl_orx_cmd) == 8, "Command must be 8 bytes");

/* Table 121 in NICIA HAS 0.83: TCP/IP EVENT_PUT format */
struct nicia_ntl_orx_resp {
	uint64_t valid : 1;
	uint64_t : 2;
	uint64_t event_type : 5;
	uint64_t fail_type : 4;
	uint64_t : 4;
	uint64_t length : 16;
	uint64_t : 32;
	uint64_t magic_number : 64;
	/* unused: 384 */
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
} __attribute__((packed, aligned(8)));

_Static_assert(sizeof(struct nicia_ntl_orx_resp) == 64, "Response must be 64 bytes");

union nicia_ptl_cmd_first_slot {
	struct nicia_raw_ptl_first_slot cmd;
	struct nicia_ni_init_cmd ni_init;
	struct nicia_ni_fini_cmd ni_fini;
	struct nicia_pte_init_cmd pte_init;
	struct nicia_pte_inv_cmd pte_inv;
	struct nicia_pte_disable_cmd pte_disable;
	struct nicia_pte_enable_cmd pte_enable;
	struct nicia_md_init_cmd md_init;
	struct nicia_md_inv_cmd md_inv;
	struct nicia_le_append_cmd le_append;
	struct nicia_me_append_slot1_cmd me_append1;
	struct nicia_unlink_cmd unlink;
	struct nicia_search_slot1_cmd search1;
	struct nicia_eq_init_cmd eq_init;
	struct nicia_eq_inv_cmd eq_inv;
	struct nicia_put_dma_cmd put_dma;
	struct nicia_get_cmd get;
	struct nicia_void_cmd _void;
	struct nicia_ni_status_cmd ni_status;
};

union nicia_ptl_cmd_second_slot {
	struct nicia_me_append_slot2_cmd me_append2;
	struct nicia_search_slot2_cmd search2;
	struct nicia_put_pio_cmd put_pio;
	struct nicia_comm_once_cmd comm_once;
};

union nicia_ptl_cmd {
	struct nicia_raw_ptl_cmd cmd;
	union nicia_ptl_cmd_first_slot first_slot;
	union nicia_ptl_cmd_second_slot second_slot;
};

union nicia_tcpip_cmd {
	struct nicia_ntl_otx_slot1_cmd ntl_otx_slot1;
	struct nicia_ntl_otx_slot2_cmd ntl_otx_slot2;
	struct nicia_ntl_otx_slot3_cmd ntl_otx_slot3;
};

union nicia_cmd {
	union nicia_ptl_cmd ptl;
	union nicia_tcpip_cmd tcpip;
	uint64_t parts[BXI3_CQ_PARTS_PER_SLOT];
};

_Static_assert(sizeof(union nicia_cmd) == 64, "Each command must be 64 bytes");

union nicia_cmd_resp {
	struct nicia_cmd_raw_resp rsp;
	struct nicia_portals_resp_cmd ptl;
	struct nicia_ni_init_resp_cmd ni_init;
	struct nicia_ni_status_resp ni_status;
};

_Static_assert(sizeof(union nicia_cmd_resp) == 64, "Each response must be 64 bytes");

struct nicia_event_generic {
	uint64_t valid : 1;
	uint64_t dropped : 1;
	uint64_t event_type : 5;
	/* unused: 149 */
	uint64_t : 21;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t mlength : 36;
	uint64_t : 64;
	uint64_t remote_offset : 57;
	/* unused: 71 */
	uint64_t : 7;
	uint64_t : 64;
	uint64_t user_ptr : 64;
	uint64_t : 64;
} __attribute__((packed, aligned(64)));

_Static_assert(sizeof(struct nicia_event_generic) == 64, "Event must be 64 bytes");

struct nicia_event_initiator {
	uint64_t valid : 1;
	uint64_t dropped : 1;
	uint64_t event_type : 5;
	uint64_t fail_type : 4;
	uint64_t : 6;
	uint64_t list_type : 1;
	/* unused: 138 */
	uint64_t : 10;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t mlength : 36;
	uint64_t : 64;
	uint64_t remote_offset : 57;
	/* unused: 71 */
	uint64_t : 7;
	uint64_t : 64;
	uint64_t user_ptr : 64;
	uint64_t : 64;
} __attribute__((packed, aligned(64)));

_Static_assert(sizeof(struct nicia_event_initiator) == 64, "Event must be 64 bytes");

struct nicia_event_target {
	uint64_t valid : 1;
	uint64_t dropped : 1;
	uint64_t event_type : 5;
	uint64_t fail_type : 4;
	uint64_t : 3;
	uint64_t atomic_operation : 5;
	uint64_t atomic_datatype : 5;
	uint64_t initiator : 40;
	uint64_t uid : 32;
	uint64_t pte_handle : 21;
	uint64_t : 3;
	uint64_t rlength : 36;
	uint64_t mlength : 36;
	uint64_t start : 57;
	uint64_t : 3;
	uint64_t hdr_data3_0 : 4;
	uint64_t remote_offset : 57;
	uint64_t : 3;
	uint64_t hdr_data7_4 : 4;
	uint64_t match_bits : 64;
	uint64_t user_ptr : 64;
	uint64_t hdr_data71_8 : 64;
} __attribute__((packed, aligned(64)));

_Static_assert(sizeof(struct nicia_event_target) == 64, "Event must be 64 bytes");

/* HAS 0.95, Table 94 */
struct nicia_event_target_control {
	uint64_t valid : 1;
	uint64_t dropped : 1;
	uint64_t event_type : 5;
	uint64_t fail_type : 4;
	uint64_t fc_err : 2;
	/* unused: 83 */
	uint64_t : 19;
	uint64_t : 64;
	uint64_t pte_handle : 21;
	/* unused: 242 */
	uint64_t : 50;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t : 64;
	uint64_t me_handle : 25;
	uint64_t user_ptr : 64;
	/* unused: 64 */
	uint64_t : 64;
} __attribute__((packed, aligned(64)));

_Static_assert(sizeof(struct nicia_event_target_control) == 64, "Event must be 64 bytes");

union nicia_event {
	struct nicia_event_generic generic;
	struct nicia_event_initiator initiator;
	struct nicia_event_target target;
	struct nicia_event_target_control target_ctrl;
};

_Static_assert(sizeof(union nicia_event) == 64, "Event must be 64 bytes");

/*
 * We are using asm code below to prevent compilers from generating unwanted or
 * non-optimal instruction sequences.
 *
 * AARCH64:
 * 1) QEMU/KVM is not supporting store+post-incr instruction on MMIO (crashes)
 * 2) It is disadvised by ARM to use store-pair on MMIO registers, but load
 * pairs are OK from normal (non-mmio) memory.
 */
#if defined(__aarch64__)
#define BXI3_WRITE_CQ_SLOT(slot, _cmd)                                                             \
	do {                                                                                       \
		asm volatile("str %x0, [%8, #8 * 0]\n"                                             \
			     "str %x1, [%8, #8 * 1]\n"                                             \
			     "str %x2, [%8, #8 * 2]\n"                                             \
			     "str %x3, [%8, #8 * 3]\n"                                             \
			     "str %x4, [%8, #8 * 4]\n"                                             \
			     "str %x5, [%8, #8 * 5]\n"                                             \
			     "str %x6, [%8, #8 * 6]\n"                                             \
			     "str %x7, [%8, #8 * 7]\n"                                             \
			     :                                                                     \
			     : "rZ"((_cmd)->parts[0]), "rZ"((_cmd)->parts[1]),                     \
			       "rZ"((_cmd)->parts[2]), "rZ"((_cmd)->parts[3]),                     \
			       "rZ"((_cmd)->parts[4]), "rZ"((_cmd)->parts[5]),                     \
			       "rZ"((_cmd)->parts[6]), "rZ"((_cmd)->parts[7]), "r"(slot));         \
	} while (0)
#elif defined(__x86_64__)
#define BXI3_WRITE_CQ_SLOT(slot, _cmd)                                                             \
	do {                                                                                       \
		asm volatile("mov (%0), %%rax;"                                                    \
			     "mov %%rax, (%1);"                                                    \
                                                                                                   \
			     "mov 0x8(%0), %%rax;"                                                 \
			     "mov %%rax, 0x8(%1);"                                                 \
                                                                                                   \
			     "mov 0x10(%0), %%rax;"                                                \
			     "mov %%rax, 0x10(%1);"                                                \
                                                                                                   \
			     "mov 0x18(%0), %%rax;"                                                \
			     "mov %%rax, 0x18(%1);"                                                \
                                                                                                   \
			     "mov 0x20(%0), %%rax;"                                                \
			     "mov %%rax, 0x20(%1);"                                                \
                                                                                                   \
			     "mov 0x28(%0), %%rax;"                                                \
			     "mov %%rax, 0x28(%1);"                                                \
                                                                                                   \
			     "mov 0x30(%0), %%rax;"                                                \
			     "mov %%rax, 0x30(%1);"                                                \
                                                                                                   \
			     "mov 0x38(%0), %%rax;"                                                \
			     "mov %%rax, 0x38(%1);"                                                \
			     :                                                                     \
			     : "r"(_cmd), "r"(slot)                                                \
			     : "%rax");                                                            \
	} while (0)
#else
#error Unsupported architecture
#endif

#define BXI3_WRITE_CQ_SLOT_IDX(cq, slot, cmd) BXI3_WRITE_CQ_SLOT(&(cq)->cq_hw->slots[slot], cmd)

#endif /* _HW_COMMAND_H */
