# Stage-3 call-path reconciliation

This appendix retains every caller row and mutation site from the supplied libyang v5.8.6
call-graph snapshot at `75158173`. Evidence codes refer to [the review report](12-retained-rb-stage3.md).
The original mapper status is retained; CHA-only edges are hypotheses, not independently proven
runtime reachability. Root transition tests apply to mapped shared primitives; unmapped APIs
have no separately claimed direct oracle case.

| Row | Native root | Complete caller chain | Original mapping status | Transition / evidence |
|---|---|---|---|---|
| 1 | `lyds_unlink` | `lyd_unlink -> lyds_unlink` | reaches | R |
| 2 | `lyds_unlink` | `_lyd_validate_op -> lyd_unlink -> lyds_unlink` | reaches; includes CHA-only candidate(s) | R |
| 3 | `lyds_unlink` | `lyd_free_tree -> lyd_unlink -> lyds_unlink (api)` | reaches | R |
| 4 | `lyds_unlink` | `lyd_insert_after -> lyd_unlink -> lyds_unlink (api)` | reaches | R |
| 5 | `lyds_unlink` | `lyd_insert_before -> lyd_unlink -> lyds_unlink (api)` | reaches; includes CHA-only candidate(s) | R |
| 6 | `lyds_unlink` | `lyd_parse_json_restconf -> lyd_unlink -> lyds_unlink` | reaches | R |
| 7 | `lyds_unlink` | `lyd_parse_value_fragment -> lyd_unlink -> lyds_unlink (api)` | no-go-mapping | R |
| 8 | `lyds_unlink` | `lyd_parse_xml_netconf -> lyd_unlink -> lyds_unlink` | reaches | R |
| 9 | `lyds_unlink` | `lyd_unlink_tree -> lyd_unlink -> lyds_unlink (api)` | reaches | R |
| 10 | `lyds_unlink` | `lyd_parse_op -> lyd_parse_json_restconf -> lyd_unlink -> lyds_unlink (api)` | reaches; includes CHA-only candidate(s) | R |
| 11 | `lyds_unlink` | `lyd_validate_op -> _lyd_validate_op -> lyd_unlink -> lyds_unlink (api)` | reaches; includes CHA-only candidate(s) | R |
| 12 | `rb_remove` | `rb_remove_node -> rb_remove` | reaches | R |
| 13 | `rb_remove` | `lyds_split -> rb_remove_node -> rb_remove` | no-go-mapping | excluded: U-0024 schema mount |
| 14 | `rb_remove` | `lyds_unlink -> rb_remove_node -> rb_remove` | reaches | R |
| 15 | `rb_remove` | `lyd_unlink -> lyds_unlink -> rb_remove_node -> rb_remove` | reaches | R |
| 16 | `rb_remove` | `lyd_unlink_siblings -> lyds_split -> rb_remove_node -> rb_remove (api)` | no-go-mapping | excluded: U-0024 schema mount |
| 17 | `rb_remove` | `_lyd_validate_op -> lyd_unlink -> lyds_unlink -> rb_remove_node -> rb_remove` | reaches; includes CHA-only candidate(s) | R |
| 18 | `rb_remove` | `lyd_free_tree -> lyd_unlink -> lyds_unlink -> rb_remove_node -> rb_remove (api)` | reaches | R |
| 19 | `rb_remove` | `lyd_insert_after -> lyd_unlink -> lyds_unlink -> rb_remove_node -> rb_remove (api)` | reaches | R |
| 20 | `rb_remove` | `lyd_insert_before -> lyd_unlink -> lyds_unlink -> rb_remove_node -> rb_remove (api)` | reaches; includes CHA-only candidate(s) | R |
| 21 | `rb_remove` | `lyd_parse_json_restconf -> lyd_unlink -> lyds_unlink -> rb_remove_node -> rb_remove` | reaches | R |
| 22 | `rb_remove` | `lyd_parse_value_fragment -> lyd_unlink -> lyds_unlink -> rb_remove_node -> rb_remove (api)` | no-go-mapping | R |
| 23 | `rb_remove` | `lyd_parse_xml_netconf -> lyd_unlink -> lyds_unlink -> rb_remove_node -> rb_remove` | reaches | R |
| 24 | `rb_remove` | `lyd_unlink_tree -> lyd_unlink -> lyds_unlink -> rb_remove_node -> rb_remove (api)` | reaches | R |
| 25 | `rb_remove` | `lyd_parse_op -> lyd_parse_json_restconf -> lyd_unlink -> lyds_unlink -> rb_remove_node -> rb_remove (api)` | reaches; includes CHA-only candidate(s) | R |
| 26 | `rb_remove` | `lyd_validate_op -> _lyd_validate_op -> lyd_unlink -> lyds_unlink -> rb_remove_node -> rb_remove (api)` | reaches; includes CHA-only candidate(s) | R |
| 27 | `lyds_free_tree` | `lyplg_type_free_lyds -> lyds_free_tree` | no-go-mapping | F (plugin callback only) |
| 28 | `lyds_free_tree` | `lyd_free_meta -> lyplg_type_free_lyds -> lyds_free_tree (fp-model)` | no-go-mapping | F (plugin callback only) |
| 29 | `lyds_free_tree` | `lyplg_type_store_lyds -> lyplg_type_free_lyds -> lyds_free_tree` | no-go-mapping | F (plugin callback only) |
| 30 | `lyds_free_tree` | `lyd_free_meta_siblings -> lyd_free_meta -> lyplg_type_free_lyds -> lyds_free_tree (api) (fp-model)` | no-go-mapping | F (plugin callback only) |
| 31 | `lyds_free_tree` | `lyd_free_meta_single -> lyd_free_meta -> lyplg_type_free_lyds -> lyds_free_tree (api) (fp-model)` | no-go-root | F (plugin callback only) |
| 32 | `lyd_unlink` | `_lyd_validate_op -> lyd_unlink` | reaches; includes CHA-only candidate(s) | R |
| 33 | `lyd_unlink` | `lyd_free_tree -> lyd_unlink (api)` | reaches; includes CHA-only candidate(s) | R |
| 34 | `lyd_unlink` | `lyd_insert_after -> lyd_unlink (api)` | reaches | R |
| 35 | `lyd_unlink` | `lyd_insert_before -> lyd_unlink (api)` | reaches; includes CHA-only candidate(s) | R |
| 36 | `lyd_unlink` | `lyd_parse_json_restconf -> lyd_unlink` | reaches | R |
| 37 | `lyd_unlink` | `lyd_parse_value_fragment -> lyd_unlink (api)` | no-go-mapping | R |
| 38 | `lyd_unlink` | `lyd_parse_xml_netconf -> lyd_unlink` | reaches | R |
| 39 | `lyd_unlink` | `lyd_unlink_tree -> lyd_unlink (api)` | reaches | R |
| 40 | `lyd_unlink` | `lyd_parse_op -> lyd_parse_json_restconf -> lyd_unlink (api)` | reaches; includes CHA-only candidate(s) | R |
| 41 | `lyd_unlink` | `lyd_validate_op -> _lyd_validate_op -> lyd_unlink (api)` | reaches; includes CHA-only candidate(s) | R |
| 42 | `lyd_free_tree` | `ly_path_eval_partial -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 43 | `lyd_free_tree` | `lyb_create_term -> lyd_free_tree` | no-go-mapping | F |
| 44 | `lyd_free_tree` | `lyb_parse_node_any -> lyd_free_tree` | no-go-mapping | F |
| 45 | `lyd_free_tree` | `lyb_parse_node_inner -> lyd_free_tree` | no-go-mapping | F |
| 46 | `lyd_free_tree` | `lyb_parse_node_leaf -> lyd_free_tree` | no-go-mapping | F |
| 47 | `lyd_free_tree` | `lyb_parse_node_list -> lyd_free_tree` | no-go-mapping | F |
| 48 | `lyd_free_tree` | `lyb_parse_node_opaq -> lyd_free_tree` | no-go-mapping | F |
| 49 | `lyd_free_tree` | `lyd_create_any -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 50 | `lyd_free_tree` | `lyd_create_list -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 51 | `lyd_free_tree` | `lyd_create_opaq -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 52 | `lyd_free_tree` | `lyd_diff_apply_r -> lyd_free_tree` | reaches | F |
| 53 | `lyd_free_tree` | `lyd_diff_merge_create -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 54 | `lyd_free_tree` | `lyd_diff_merge_r -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 55 | `lyd_free_tree` | `lyd_dup -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 56 | `lyd_free_tree` | `lyd_dup_r -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 57 | `lyd_free_tree` | `lyd_find_sibling_val -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 58 | `lyd_free_tree` | `lyd_new_list -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 59 | `lyd_free_tree` | `lyd_new_list3 -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 60 | `lyd_free_tree` | `lyd_new_path_create -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 61 | `lyd_free_tree` | `lyd_new_path_update -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 62 | `lyd_free_tree` | `lyd_new_term_raw -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 63 | `lyd_free_tree` | `lyd_parse -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 64 | `lyd_free_tree` | `lyd_parse_op -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 65 | `lyd_free_tree` | `lyd_parse_value_fragment -> lyd_free_tree (api)` | no-go-mapping | F |
| 66 | `lyd_free_tree` | `lyd_parse_xml -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 67 | `lyd_free_tree` | `lyd_parser_node_free -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 68 | `lyd_free_tree` | `lyd_trim_xpath -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 69 | `lyd_free_tree` | `lyd_val_diff_add -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 70 | `lyd_free_tree` | `lyd_validate_autodel_node_del -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 71 | `lyd_free_tree` | `lyd_validate_cases -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 72 | `lyd_free_tree` | `lyd_validate_dummy_when -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 73 | `lyd_free_tree` | `lydjson_envelope -> lyd_free_tree` | no-go-mapping | F |
| 74 | `lyd_free_tree` | `lydjson_metadata_finish -> lyd_free_tree` | no-go-mapping | F |
| 75 | `lyd_free_tree` | `lydxml_env_netconf_reply -> lyd_free_tree` | no-go-mapping | F |
| 76 | `lyd_free_tree` | `lydxml_env_netconf_rpc -> lyd_free_tree` | no-go-mapping | F |
| 77 | `lyd_free_tree` | `lydxml_envelope -> lyd_free_tree` | no-go-mapping | F |
| 78 | `lyd_free_tree` | `moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 79 | `lyd_free_tree` | `schema_mount_dup_parent_ref -> lyd_free_tree` | no-go-mapping | excluded: U-0024 schema mount |
| 80 | `lyd_free_tree` | `schema_mount_validate -> lyd_free_tree` | no-go-mapping | excluded: U-0024 schema mount |
| 81 | `lyd_free_tree` | `eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 82 | `lyd_free_tree` | `ly_path_eval -> ly_path_eval_partial -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 83 | `lyd_free_tree` | `lyb_parse_node -> lyb_parse_node_any -> lyd_free_tree` | no-go-mapping | F |
| 84 | `lyd_free_tree` | `lyb_parse_node_leaflist -> lyb_parse_node_leaf -> lyd_free_tree` | no-go-mapping | F |
| 85 | `lyd_free_tree` | `lyd_create_any_datatree -> lyd_parse_xml -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 86 | `lyd_free_tree` | `lyd_create_list2 -> lyd_create_list -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 87 | `lyd_free_tree` | `lyd_diff_apply_module -> lyd_diff_apply_r -> lyd_free_tree (api)` | reaches | F |
| 88 | `lyd_free_tree` | `lyd_diff_merge_module -> lyd_diff_merge_r -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 89 | `lyd_free_tree` | `lyd_diff_merge_tree -> lyd_diff_merge_r -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 90 | `lyd_free_tree` | `lyd_dup_get_local_parent -> lyd_dup_r -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 91 | `lyd_free_tree` | `lyd_dup_siblings -> lyd_dup -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 92 | `lyd_free_tree` | `lyd_dup_siblings_to_ctx -> lyd_dup -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 93 | `lyd_free_tree` | `lyd_dup_single -> lyd_dup -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 94 | `lyd_free_tree` | `lyd_dup_single_to_ctx -> lyd_dup -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 95 | `lyd_free_tree` | `lyd_find_path -> ly_path_eval_partial -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 96 | `lyd_free_tree` | `lyd_new_any -> lyd_create_any -> lyd_free_tree (api)` | no-go-mapping | F |
| 97 | `lyd_free_tree` | `lyd_new_implicit -> lyd_val_diff_add -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 98 | `lyd_free_tree` | `lyd_new_opaq -> lyd_create_opaq -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 99 | `lyd_free_tree` | `lyd_new_opaq2 -> lyd_create_opaq -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 100 | `lyd_free_tree` | `lyd_new_path_ -> lyd_new_path_create -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 101 | `lyd_free_tree` | `lyd_parse_data -> lyd_parse -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 102 | `lyd_free_tree` | `lyd_parse_json -> lydjson_metadata_finish -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 103 | `lyd_free_tree` | `lyd_parse_json_bare_value -> lyd_parser_node_free -> lyd_free_tree` | no-go-mapping | F |
| 104 | `lyd_free_tree` | `lyd_parse_json_restconf -> lydjson_envelope -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 105 | `lyd_free_tree` | `lyd_parse_xml_netconf -> lydxml_env_netconf_reply -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 106 | `lyd_free_tree` | `lyd_validate_autodel_case_dflt -> lyd_validate_autodel_node_del -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 107 | `lyd_free_tree` | `lyd_validate_autodel_cont_leaf_dflt -> lyd_validate_autodel_node_del -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 108 | `lyd_free_tree` | `lyd_validate_autodel_leaflist_dflt -> lyd_validate_autodel_node_del -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 109 | `lyd_free_tree` | `lyd_validate_choice_r -> lyd_validate_cases -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 110 | `lyd_free_tree` | `lyd_validate_mandatory -> lyd_validate_dummy_when -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 111 | `lyd_free_tree` | `lyd_validate_minmax -> lyd_validate_dummy_when -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 112 | `lyd_free_tree` | `lyd_validate_unres_when -> lyd_validate_autodel_node_del -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 113 | `lyd_free_tree` | `lydjson_create_opaq -> lyd_create_opaq -> lyd_free_tree` | no-go-mapping | F |
| 114 | `lyd_free_tree` | `lydjson_parse_any -> lyd_create_any -> lyd_free_tree` | no-go-mapping | F |
| 115 | `lyd_free_tree` | `lydjson_parse_instance_inner -> lyd_parser_node_free -> lyd_free_tree` | no-go-mapping | F |
| 116 | `lyd_free_tree` | `lydjson_parse_opaq -> lydjson_metadata_finish -> lyd_free_tree` | no-go-mapping | F |
| 117 | `lyd_free_tree` | `lydjson_subtree_r -> lyd_create_opaq -> lyd_free_tree` | no-go-mapping | F |
| 118 | `lyd_free_tree` | `lydxml_subtree_any -> lyd_create_any -> lyd_free_tree` | no-go-mapping | F |
| 119 | `lyd_free_tree` | `lydxml_subtree_inner -> lyd_parser_node_free -> lyd_free_tree` | no-go-mapping | F |
| 120 | `lyd_free_tree` | `lydxml_subtree_opaq -> lyd_create_opaq -> lyd_free_tree` | no-go-mapping | F |
| 121 | `lyd_free_tree` | `lydxml_subtree_r -> lyd_create_opaq -> lyd_free_tree` | no-go-mapping | F |
| 122 | `lyd_free_tree` | `lydxml_subtree_term -> lyd_parser_node_free -> lyd_free_tree` | no-go-mapping | F |
| 123 | `lyd_free_tree` | `_lyd_validate_op -> lyd_new_implicit -> lyd_val_diff_add -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 124 | `lyd_free_tree` | `eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 125 | `lyd_free_tree` | `lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyd_free_tree` | no-go-mapping | F |
| 126 | `lyd_free_tree` | `lyd_any_copy_value -> lyd_create_any_datatree -> lyd_parse_xml -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 127 | `lyd_free_tree` | `lyd_find_target -> ly_path_eval -> ly_path_eval_partial -> lyd_free_tree (api)` | no-go-mapping | F |
| 128 | `lyd_free_tree` | `lyd_new_implicit_module -> lyd_new_implicit -> lyd_val_diff_add -> lyd_free_tree (api)` | no-go-mapping | F |
| 129 | `lyd_free_tree` | `lyd_new_implicit_r -> lyd_new_implicit -> lyd_val_diff_add -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 130 | `lyd_free_tree` | `lyd_new_implicit_tree -> lyd_new_implicit -> lyd_val_diff_add -> lyd_free_tree (api)` | no-go-mapping | F |
| 131 | `lyd_free_tree` | `lyd_new_list2 -> lyd_create_list2 -> lyd_create_list -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 132 | `lyd_free_tree` | `lyd_new_path -> lyd_new_path_ -> lyd_new_path_create -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 133 | `lyd_free_tree` | `lyd_new_path2 -> lyd_new_path_ -> lyd_new_path_create -> lyd_free_tree (api)` | no-go-mapping | F |
| 134 | `lyd_free_tree` | `lyd_validate -> lyd_new_implicit -> lyd_val_diff_add -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 135 | `lyd_free_tree` | `lyd_validate_new -> lyd_validate_autodel_case_dflt -> lyd_validate_autodel_node_del -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 136 | `lyd_free_tree` | `lyd_validate_siblings_schema_r -> lyd_validate_mandatory -> lyd_validate_dummy_when -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 137 | `lyd_free_tree` | `lyd_validate_tree -> lyd_new_implicit -> lyd_val_diff_add -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 138 | `lyd_free_tree` | `lyd_validate_unres -> lyd_validate_unres_when -> lyd_validate_autodel_node_del -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 139 | `lyd_free_tree` | `lydjson_ctx_next_parse_opaq -> lydjson_parse_opaq -> lydjson_metadata_finish -> lyd_free_tree` | no-go-mapping | F |
| 140 | `lyd_free_tree` | `lydjson_parse_instance -> lydjson_parse_any -> lyd_create_any -> lyd_free_tree` | no-go-mapping | F |
| 141 | `lyd_free_tree` | `lyplg_type_validate_tree_instanceid -> ly_path_eval -> ly_path_eval_partial -> lyd_free_tree` | no-go-mapping | F |
| 142 | `lyd_free_tree` | `xpath_deref_type -> ly_path_eval -> ly_path_eval_partial -> lyd_free_tree` | no-go-mapping | F |
| 143 | `lyd_free_tree` | `eval_absolute_location_path -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 144 | `lyd_free_tree` | `eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 145 | `lyd_free_tree` | `lyd_parse_lyb -> lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyd_free_tree` | no-go-mapping | F |
| 146 | `lyd_free_tree` | `lyd_parser_validate_new_implicit -> lyd_new_implicit_r -> lyd_new_implicit -> lyd_val_diff_add -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 147 | `lyd_free_tree` | `lyd_validate_all -> lyd_validate -> lyd_new_implicit -> lyd_val_diff_add -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 148 | `lyd_free_tree` | `lyd_validate_ext -> lyd_validate_tree -> lyd_new_implicit -> lyd_val_diff_add -> lyd_free_tree (api)` | no-go-mapping | F |
| 149 | `lyd_free_tree` | `lyd_validate_final_r -> lyd_validate_siblings_schema_r -> lyd_validate_mandatory -> lyd_validate_dummy_when -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 150 | `lyd_free_tree` | `lyd_validate_module -> lyd_validate -> lyd_new_implicit -> lyd_val_diff_add -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 151 | `lyd_free_tree` | `lyd_validate_op -> _lyd_validate_op -> lyd_new_implicit -> lyd_val_diff_add -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 152 | `lyd_free_tree` | `lydjson_parse_attribute -> lydjson_ctx_next_parse_opaq -> lydjson_parse_opaq -> lydjson_metadata_finish -> lyd_free_tree` | no-go-mapping | F |
| 153 | `lyd_free_tree` | `xpath_deref -> xpath_deref_type -> ly_path_eval -> ly_path_eval_partial -> lyd_free_tree` | no-go-mapping | F |
| 154 | `lyd_free_tree` | `eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 155 | `lyd_free_tree` | `lyb_validate_node_inner -> lyd_parser_validate_new_implicit -> lyd_new_implicit_r -> lyd_new_implicit -> lyd_val_diff_add -> lyd_free_tree` | no-go-mapping | F |
| 156 | `lyd_free_tree` | `lyd_validate_module_final -> lyd_validate_final_r -> lyd_validate_siblings_schema_r -> lyd_validate_mandatory -> lyd_validate_dummy_when -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 157 | `lyd_free_tree` | `eval_additive_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 158 | `lyd_free_tree` | `eval_and_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 159 | `lyd_free_tree` | `eval_equality_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 160 | `lyd_free_tree` | `eval_function_call -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 161 | `lyd_free_tree` | `eval_multiplicative_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 162 | `lyd_free_tree` | `eval_or_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 163 | `lyd_free_tree` | `eval_predicate -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 164 | `lyd_free_tree` | `eval_relational_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 165 | `lyd_free_tree` | `eval_unary_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 166 | `lyd_free_tree` | `eval_union_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 167 | `lyd_free_tree` | `eval_variable_reference -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 168 | `lyd_free_tree` | `lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 169 | `lyd_free_tree` | `lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 170 | `lyd_free_tree` | `eval_name_test_try_compile_predicate_append -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 171 | `lyd_free_tree` | `eval_node_type_with_predicate -> eval_predicate -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 172 | `lyd_free_tree` | `lyd_eval_xpath4 -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 173 | `lyd_free_tree` | `lyd_validate_must -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 174 | `lyd_free_tree` | `lyd_validate_node_when -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | reaches; includes CHA-only candidate(s) | F |
| 175 | `lyd_free_tree` | `lyplg_type_resolve_leafref -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree (api)` | no-go-mapping | F |
| 176 | `lyd_free_tree` | `lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 177 | `lyd_free_tree` | `lys_compile_unres_when -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 178 | `lyd_free_tree` | `lys_compile_unres_when_cyclic -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 179 | `lyd_free_tree` | `lys_find_expr_atoms -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree (api)` | no-go-mapping | F |
| 180 | `lyd_free_tree` | `lys_find_xpath -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree (api)` | no-go-mapping | F |
| 181 | `lyd_free_tree` | `lys_find_xpath_atoms -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree (api)` | no-go-mapping | F |
| 182 | `lyd_free_tree` | `eval_name_test_try_compile_predicates -> eval_name_test_try_compile_predicate_append -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 183 | `lyd_free_tree` | `lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 184 | `lyd_free_tree` | `lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 185 | `lyd_free_tree` | `lys_compile_depset_all -> lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 186 | `lyd_free_tree` | `ly_ctx_compile -> lys_compile_depset_all -> lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 187 | `lyd_free_tree` | `ly_ctx_load_module -> lys_compile_depset_all -> lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree (api)` | reaches; includes CHA-only candidate(s) | F |
| 188 | `lyd_free_tree` | `lys_parse -> lys_compile_depset_all -> lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree (api)` | no-go-mapping | F |
| 189 | `lyd_free_tree` | `lys_set_implemented -> lys_compile_depset_all -> lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree (api)` | no-go-mapping | F |
| 190 | `lyd_free_tree` | `lys_unres_glob_revert -> lys_compile_depset_all -> lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_free_tree` | no-go-mapping | F |
| 191 | `lyd_dup_r` | `lyd_dup -> lyd_dup_r` | reaches | D |
| 192 | `lyd_dup_r` | `lyd_dup_get_local_parent -> lyd_dup_r` | reaches | D |
| 193 | `lyd_dup_r` | `lyd_dup_siblings -> lyd_dup -> lyd_dup_r (api)` | reaches | D |
| 194 | `lyd_dup_r` | `lyd_dup_siblings_to_ctx -> lyd_dup -> lyd_dup_r (api)` | reaches | D |
| 195 | `lyd_dup_r` | `lyd_dup_single -> lyd_dup -> lyd_dup_r (api)` | reaches | D |
| 196 | `lyd_dup_r` | `lyd_dup_single_to_ctx -> lyd_dup -> lyd_dup_r (api)` | reaches | D |
| 197 | `lyd_parser_node_insert` | `lydjson_parse_any -> lyd_parser_node_insert` | no-go-mapping | P |
| 198 | `lyd_parser_node_insert` | `lydjson_parse_instance_inner -> lyd_parser_node_insert` | no-go-mapping | P |
| 199 | `lyd_parser_node_insert` | `lydjson_parse_instance_term -> lyd_parser_node_insert` | no-go-mapping | P |
| 200 | `lyd_parser_node_insert` | `lydjson_parse_opaq -> lyd_parser_node_insert` | no-go-mapping | P |
| 201 | `lyd_parser_node_insert` | `lydjson_subtree_r -> lyd_parser_node_insert` | no-go-mapping | P |
| 202 | `lyd_parser_node_insert` | `lydxml_subtree_any -> lyd_parser_node_insert` | no-go-mapping | P |
| 203 | `lyd_parser_node_insert` | `lydxml_subtree_inner -> lyd_parser_node_insert` | no-go-mapping | P |
| 204 | `lyd_parser_node_insert` | `lydxml_subtree_opaq -> lyd_parser_node_insert` | no-go-mapping | P |
| 205 | `lyd_parser_node_insert` | `lydxml_subtree_r -> lyd_parser_node_insert` | no-go-mapping | P |
| 206 | `lyd_parser_node_insert` | `lydxml_subtree_term -> lyd_parser_node_insert` | no-go-mapping | P |
| 207 | `lyd_parser_node_insert` | `lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | reaches | P |
| 208 | `lyd_parser_node_insert` | `lyd_parse_json_restconf -> lydjson_subtree_r -> lyd_parser_node_insert` | reaches | P |
| 209 | `lyd_parser_node_insert` | `lyd_parse_xml -> lydxml_subtree_r -> lyd_parser_node_insert` | reaches | P |
| 210 | `lyd_parser_node_insert` | `lyd_parse_xml_netconf -> lydxml_subtree_r -> lyd_parser_node_insert` | reaches | P |
| 211 | `lyd_parser_node_insert` | `lydjson_ctx_next_parse_opaq -> lydjson_parse_opaq -> lyd_parser_node_insert` | no-go-mapping | P |
| 212 | `lyd_parser_node_insert` | `lydjson_parse_instance -> lydjson_parse_any -> lyd_parser_node_insert` | no-go-mapping | P |
| 213 | `lyd_parser_node_insert` | `lydxml_env_netconf_reply -> lydxml_subtree_r -> lyd_parser_node_insert` | no-go-mapping | P |
| 214 | `lyd_parser_node_insert` | `lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | reaches | P |
| 215 | `lyd_parser_node_insert` | `lyd_parse -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | reaches | P |
| 216 | `lyd_parser_node_insert` | `lyd_parse_json_bare_value -> lydjson_parse_instance -> lydjson_parse_any -> lyd_parser_node_insert` | no-go-mapping | P |
| 217 | `lyd_parser_node_insert` | `lyd_parse_op -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert (api)` | reaches; includes CHA-only candidate(s) | P |
| 218 | `lyd_parser_node_insert` | `lyd_parse_value_fragment -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert (api)` | no-go-mapping | P |
| 219 | `lyd_parser_node_insert` | `lydjson_parse_attribute -> lydjson_ctx_next_parse_opaq -> lydjson_parse_opaq -> lyd_parser_node_insert` | no-go-mapping | P |
| 220 | `lyd_parser_node_insert` | `lyd_any_copy_value -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert (api)` | reaches | P |
| 221 | `lyd_parser_node_insert` | `lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | reaches | P |
| 222 | `lyd_parser_node_insert` | `lyd_parse_data -> lyd_parse -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert (api)` | reaches | P |
| 223 | `lyd_parser_node_insert` | `lyb_parse_node_any -> lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | no-go-mapping | P |
| 224 | `lyd_parser_node_insert` | `lyd_new_any -> lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert (api)` | no-go-mapping | P |
| 225 | `lyd_parser_node_insert` | `lyd_new_path_create -> lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | reaches; includes CHA-only candidate(s) | P |
| 226 | `lyd_parser_node_insert` | `lyd_new_path_update -> lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | reaches; includes CHA-only candidate(s) | P |
| 227 | `lyd_parser_node_insert` | `lyb_parse_node -> lyb_parse_node_any -> lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | no-go-mapping | P |
| 228 | `lyd_parser_node_insert` | `lyd_new_path_ -> lyd_new_path_create -> lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | reaches; includes CHA-only candidate(s) | P |
| 229 | `lyd_parser_node_insert` | `lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | no-go-mapping | P |
| 230 | `lyd_parser_node_insert` | `lyd_new_path -> lyd_new_path_ -> lyd_new_path_create -> lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert (api)` | reaches; includes CHA-only candidate(s) | P |
| 231 | `lyd_parser_node_insert` | `lyd_new_path2 -> lyd_new_path_ -> lyd_new_path_create -> lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert (api)` | no-go-mapping | P |
| 232 | `lyd_parser_node_insert` | `lyb_parse_node_inner -> lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | no-go-mapping | P |
| 233 | `lyd_parser_node_insert` | `lyb_parse_node_list -> lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | no-go-mapping | P |
| 234 | `lyd_parser_node_insert` | `lyb_parse_node_opaq -> lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | no-go-mapping | P |
| 235 | `lyd_parser_node_insert` | `lyd_parse_lyb -> lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert` | no-go-mapping | P |
| 236 | `lyd_change_term_val` | `_lyd_change_term -> lyd_change_term_val` | reaches; includes CHA-only candidate(s) | V |
| 237 | `lyd_change_term_val` | `lyd_merge_sibling_r -> lyd_change_term_val` | reaches | V |
| 238 | `lyd_change_term_val` | `lyd_change_term -> _lyd_change_term -> lyd_change_term_val (api)` | reaches | V |
| 239 | `lyd_change_term_val` | `lyd_change_term_canon -> _lyd_change_term -> lyd_change_term_val (api)` | reaches | V |
| 240 | `lyd_change_term_val` | `lyd_merge -> lyd_merge_sibling_r -> lyd_change_term_val` | reaches; includes CHA-only candidate(s) | V |
| 241 | `lyd_change_term_val` | `lyd_new_path_update -> _lyd_change_term -> lyd_change_term_val` | reaches; includes CHA-only candidate(s) | V |
| 242 | `lyd_change_term_val` | `lyd_merge_module -> lyd_merge -> lyd_merge_sibling_r -> lyd_change_term_val (api)` | no-go-mapping | V |
| 243 | `lyd_change_term_val` | `lyd_merge_siblings -> lyd_merge -> lyd_merge_sibling_r -> lyd_change_term_val (api)` | reaches | V |
| 244 | `lyd_change_term_val` | `lyd_merge_tree -> lyd_merge -> lyd_merge_sibling_r -> lyd_change_term_val (api)` | no-go-mapping | V |
| 245 | `lyd_change_term_val` | `lyd_new_path_create -> lyd_new_path_update -> _lyd_change_term -> lyd_change_term_val` | reaches; includes CHA-only candidate(s) | V |
| 246 | `lyd_change_term_val` | `lyd_new_path_ -> lyd_new_path_create -> lyd_new_path_update -> _lyd_change_term -> lyd_change_term_val` | reaches; includes CHA-only candidate(s) | V |
| 247 | `lyd_change_term_val` | `lyd_parse_value_fragment -> lyd_new_path_create -> lyd_new_path_update -> _lyd_change_term -> lyd_change_term_val (api)` | no-go-mapping | V |
| 248 | `lyd_change_term_val` | `lyd_new_path -> lyd_new_path_ -> lyd_new_path_create -> lyd_new_path_update -> _lyd_change_term -> lyd_change_term_val (api)` | reaches; includes CHA-only candidate(s) | V |
| 249 | `lyd_change_term_val` | `lyd_new_path2 -> lyd_new_path_ -> lyd_new_path_create -> lyd_new_path_update -> _lyd_change_term -> lyd_change_term_val (api)` | no-go-mapping | V |
| 250 | `lyd_change_node_value` | `lyd_change_term_val -> lyd_change_node_value` | reaches | V |
| 251 | `lyd_change_node_value` | `_lyd_change_term -> lyd_change_term_val -> lyd_change_node_value` | reaches | V |
| 252 | `lyd_change_node_value` | `lyd_merge_sibling_r -> lyd_change_term_val -> lyd_change_node_value` | reaches | V |
| 253 | `lyd_change_node_value` | `lyd_change_term -> _lyd_change_term -> lyd_change_term_val -> lyd_change_node_value (api)` | reaches | V |
| 254 | `lyd_change_node_value` | `lyd_change_term_canon -> _lyd_change_term -> lyd_change_term_val -> lyd_change_node_value (api)` | reaches | V |
| 255 | `lyd_change_node_value` | `lyd_merge -> lyd_merge_sibling_r -> lyd_change_term_val -> lyd_change_node_value` | reaches; includes CHA-only candidate(s) | V |
| 256 | `lyd_change_node_value` | `lyd_new_path_update -> _lyd_change_term -> lyd_change_term_val -> lyd_change_node_value` | reaches; includes CHA-only candidate(s) | V |
| 257 | `lyd_change_node_value` | `lyd_merge_module -> lyd_merge -> lyd_merge_sibling_r -> lyd_change_term_val -> lyd_change_node_value (api)` | no-go-mapping | V |
| 258 | `lyd_change_node_value` | `lyd_merge_siblings -> lyd_merge -> lyd_merge_sibling_r -> lyd_change_term_val -> lyd_change_node_value (api)` | reaches | V |
| 259 | `lyd_change_node_value` | `lyd_merge_tree -> lyd_merge -> lyd_merge_sibling_r -> lyd_change_term_val -> lyd_change_node_value (api)` | no-go-mapping | V |
| 260 | `lyd_change_node_value` | `lyd_new_path_create -> lyd_new_path_update -> _lyd_change_term -> lyd_change_term_val -> lyd_change_node_value` | reaches; includes CHA-only candidate(s) | V |
| 261 | `lyd_change_node_value` | `lyd_new_path_ -> lyd_new_path_create -> lyd_new_path_update -> _lyd_change_term -> lyd_change_term_val -> lyd_change_node_value` | reaches; includes CHA-only candidate(s) | V |
| 262 | `lyd_change_node_value` | `lyd_parse_value_fragment -> lyd_new_path_create -> lyd_new_path_update -> _lyd_change_term -> lyd_change_term_val -> lyd_change_node_value (api)` | no-go-mapping | V |
| 263 | `lyd_change_node_value` | `lyd_new_path -> lyd_new_path_ -> lyd_new_path_create -> lyd_new_path_update -> _lyd_change_term -> lyd_change_term_val -> lyd_change_node_value (api)` | reaches; includes CHA-only candidate(s) | V |
| 264 | `lyd_change_node_value` | `lyd_new_path2 -> lyd_new_path_ -> lyd_new_path_create -> lyd_new_path_update -> _lyd_change_term -> lyd_change_term_val -> lyd_change_node_value (api)` | no-go-mapping | V |
| 265 | `lyd_any_copy_value` | `lyd_create_any -> lyd_any_copy_value` | reaches | A |
| 266 | `lyd_any_copy_value` | `lyd_diff_apply_r -> lyd_any_copy_value` | reaches | A |
| 267 | `lyd_any_copy_value` | `lyd_diff_merge_replace -> lyd_any_copy_value` | reaches | A |
| 268 | `lyd_any_copy_value` | `lyd_diff_reverse_value -> lyd_any_copy_value` | reaches | A |
| 269 | `lyd_any_copy_value` | `lyd_dup_r -> lyd_any_copy_value` | reaches | A |
| 270 | `lyd_any_copy_value` | `lyd_free_subtree -> lyd_any_copy_value` | reaches; includes CHA-only candidate(s) | A |
| 271 | `lyd_any_copy_value` | `lyd_merge_sibling_r -> lyd_any_copy_value` | reaches | A |
| 272 | `lyd_any_copy_value` | `lyb_parse_node_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 273 | `lyd_any_copy_value` | `lyd_diff_apply_module -> lyd_diff_apply_r -> lyd_any_copy_value (api)` | reaches | A |
| 274 | `lyd_any_copy_value` | `lyd_diff_merge_r -> lyd_diff_merge_replace -> lyd_any_copy_value` | reaches | A |
| 275 | `lyd_any_copy_value` | `lyd_diff_reverse_siblings_r -> lyd_diff_reverse_value -> lyd_any_copy_value` | reaches | A |
| 276 | `lyd_any_copy_value` | `lyd_dup -> lyd_dup_r -> lyd_any_copy_value` | reaches | A |
| 277 | `lyd_any_copy_value` | `lyd_dup_get_local_parent -> lyd_dup_r -> lyd_any_copy_value` | reaches | A |
| 278 | `lyd_any_copy_value` | `lyd_free_ -> lyd_free_subtree -> lyd_any_copy_value` | does-not-reach | A |
| 279 | `lyd_any_copy_value` | `lyd_free_tree -> lyd_free_subtree -> lyd_any_copy_value (api)` | reaches; includes CHA-only candidate(s) | A |
| 280 | `lyd_any_copy_value` | `lyd_merge -> lyd_merge_sibling_r -> lyd_any_copy_value` | reaches | A |
| 281 | `lyd_any_copy_value` | `lyd_new_any -> lyd_create_any -> lyd_any_copy_value (api)` | no-go-mapping | A |
| 282 | `lyd_any_copy_value` | `lyd_new_path_create -> lyd_create_any -> lyd_any_copy_value` | reaches; includes CHA-only candidate(s) | A |
| 283 | `lyd_any_copy_value` | `lyd_new_path_update -> lyd_create_any -> lyd_any_copy_value` | reaches; includes CHA-only candidate(s) | A |
| 284 | `lyd_any_copy_value` | `lydjson_parse_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 285 | `lyd_any_copy_value` | `lydxml_subtree_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 286 | `lyd_any_copy_value` | `lyb_parse_node -> lyb_parse_node_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 287 | `lyd_any_copy_value` | `lyd_diff_merge_module -> lyd_diff_merge_r -> lyd_diff_merge_replace -> lyd_any_copy_value (api)` | reaches | A |
| 288 | `lyd_any_copy_value` | `lyd_diff_merge_tree -> lyd_diff_merge_r -> lyd_diff_merge_replace -> lyd_any_copy_value (api)` | reaches | A |
| 289 | `lyd_any_copy_value` | `lyd_diff_reverse_all -> lyd_diff_reverse_siblings_r -> lyd_diff_reverse_value -> lyd_any_copy_value (api)` | reaches | A |
| 290 | `lyd_any_copy_value` | `lyd_dup_siblings -> lyd_dup -> lyd_dup_r -> lyd_any_copy_value (api)` | reaches | A |
| 291 | `lyd_any_copy_value` | `lyd_dup_siblings_to_ctx -> lyd_dup -> lyd_dup_r -> lyd_any_copy_value (api)` | reaches | A |
| 292 | `lyd_any_copy_value` | `lyd_dup_single -> lyd_dup -> lyd_dup_r -> lyd_any_copy_value (api)` | reaches | A |
| 293 | `lyd_any_copy_value` | `lyd_dup_single_to_ctx -> lyd_dup -> lyd_dup_r -> lyd_any_copy_value (api)` | reaches | A |
| 294 | `lyd_any_copy_value` | `lyd_free_all -> lyd_free_ -> lyd_free_subtree -> lyd_any_copy_value (api)` | does-not-reach | A |
| 295 | `lyd_any_copy_value` | `lyd_free_siblings -> lyd_free_ -> lyd_free_subtree -> lyd_any_copy_value (api)` | reaches; includes CHA-only candidate(s) | A |
| 296 | `lyd_any_copy_value` | `lyd_merge_module -> lyd_merge -> lyd_merge_sibling_r -> lyd_any_copy_value (api)` | no-go-mapping | A |
| 297 | `lyd_any_copy_value` | `lyd_merge_siblings -> lyd_merge -> lyd_merge_sibling_r -> lyd_any_copy_value (api)` | reaches | A |
| 298 | `lyd_any_copy_value` | `lyd_merge_tree -> lyd_merge -> lyd_merge_sibling_r -> lyd_any_copy_value (api)` | no-go-mapping | A |
| 299 | `lyd_any_copy_value` | `lyd_new_path_ -> lyd_new_path_create -> lyd_create_any -> lyd_any_copy_value` | reaches; includes CHA-only candidate(s) | A |
| 300 | `lyd_any_copy_value` | `lyd_parse_value_fragment -> lyd_new_path_create -> lyd_create_any -> lyd_any_copy_value (api)` | no-go-mapping | A |
| 301 | `lyd_any_copy_value` | `lydjson_parse_instance -> lydjson_parse_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 302 | `lyd_any_copy_value` | `lydxml_subtree_r -> lydxml_subtree_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 303 | `lyd_any_copy_value` | `lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 304 | `lyd_any_copy_value` | `lyd_new_path -> lyd_new_path_ -> lyd_new_path_create -> lyd_create_any -> lyd_any_copy_value (api)` | reaches; includes CHA-only candidate(s) | A |
| 305 | `lyd_any_copy_value` | `lyd_new_path2 -> lyd_new_path_ -> lyd_new_path_create -> lyd_create_any -> lyd_any_copy_value (api)` | no-go-mapping | A |
| 306 | `lyd_any_copy_value` | `lyd_parse_json_bare_value -> lydjson_parse_instance -> lydjson_parse_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 307 | `lyd_any_copy_value` | `lyd_parse_xml -> lydxml_subtree_r -> lydxml_subtree_any -> lyd_create_any -> lyd_any_copy_value` | reaches | A |
| 308 | `lyd_any_copy_value` | `lyd_parse_xml_netconf -> lydxml_subtree_r -> lydxml_subtree_any -> lyd_create_any -> lyd_any_copy_value` | reaches | A |
| 309 | `lyd_any_copy_value` | `lydjson_subtree_r -> lydjson_parse_instance -> lydjson_parse_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 310 | `lyd_any_copy_value` | `lydxml_env_netconf_reply -> lydxml_subtree_r -> lydxml_subtree_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 311 | `lyd_any_copy_value` | `lydxml_subtree_inner -> lydxml_subtree_r -> lydxml_subtree_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 312 | `lyd_any_copy_value` | `lydxml_subtree_opaq -> lydxml_subtree_r -> lydxml_subtree_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 313 | `lyd_any_copy_value` | `lyb_parse_node_inner -> lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 314 | `lyd_any_copy_value` | `lyb_parse_node_list -> lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 315 | `lyd_any_copy_value` | `lyb_parse_node_opaq -> lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 316 | `lyd_any_copy_value` | `lyd_create_any_datatree -> lyd_parse_xml -> lydxml_subtree_r -> lydxml_subtree_any -> lyd_create_any -> lyd_any_copy_value` | reaches | A |
| 317 | `lyd_any_copy_value` | `lyd_parse -> lyd_parse_xml -> lydxml_subtree_r -> lydxml_subtree_any -> lyd_create_any -> lyd_any_copy_value` | reaches | A |
| 318 | `lyd_any_copy_value` | `lyd_parse_json -> lyd_parse_json_bare_value -> lydjson_parse_instance -> lydjson_parse_any -> lyd_create_any -> lyd_any_copy_value` | reaches | A |
| 319 | `lyd_any_copy_value` | `lyd_parse_json_restconf -> lydjson_subtree_r -> lydjson_parse_instance -> lydjson_parse_any -> lyd_create_any -> lyd_any_copy_value` | reaches | A |
| 320 | `lyd_any_copy_value` | `lyd_parse_lyb -> lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 321 | `lyd_any_copy_value` | `lyd_parse_op -> lyd_parse_xml -> lydxml_subtree_r -> lydxml_subtree_any -> lyd_create_any -> lyd_any_copy_value (api)` | reaches; includes CHA-only candidate(s) | A |
| 322 | `lyd_any_copy_value` | `lydjson_parse_instance_inner -> lydjson_subtree_r -> lydjson_parse_instance -> lydjson_parse_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 323 | `lyd_any_copy_value` | `lydjson_parse_opaq -> lydjson_subtree_r -> lydjson_parse_instance -> lydjson_parse_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 324 | `lyd_any_copy_value` | `lyd_parse_data -> lyd_parse -> lyd_parse_xml -> lydxml_subtree_r -> lydxml_subtree_any -> lyd_create_any -> lyd_any_copy_value (api)` | reaches | A |
| 325 | `lyd_any_copy_value` | `lydjson_ctx_next_parse_opaq -> lydjson_parse_opaq -> lydjson_subtree_r -> lydjson_parse_instance -> lydjson_parse_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 326 | `lyd_any_copy_value` | `lydjson_parse_attribute -> lydjson_ctx_next_parse_opaq -> lydjson_parse_opaq -> lydjson_subtree_r -> lydjson_parse_instance -> lydjson_parse_any -> lyd_create_any -> lyd_any_copy_value` | no-go-mapping | A |
| 327 | `lyd_unlink_siblings` | `schema_mount_validate -> lyd_unlink_siblings` | no-go-mapping | excluded: U-0024 schema mount |
| 328 | `lyds_insert` | `lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 329 | `lyds_insert` | `_lyd_new_term -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 330 | `lyds_insert` | `_lyd_validate_op -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 331 | `lyds_insert` | `lyb_insert_node -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 332 | `lyds_insert` | `lyd_change_node_value -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 333 | `lyds_insert` | `lyd_create_list -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 334 | `lyds_insert` | `lyd_diff_dup -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 335 | `lyds_insert` | `lyd_diff_insert_sibling -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 336 | `lyds_insert` | `lyd_dup_get_local_parent -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 337 | `lyds_insert` | `lyd_dup_r -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 338 | `lyds_insert` | `lyd_insert_child -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 339 | `lyds_insert` | `lyd_insert_sibling -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 340 | `lyds_insert` | `lyd_merge_sibling_r -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 341 | `lyds_insert` | `lyd_new_any -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 342 | `lyds_insert` | `lyd_new_implicit -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 343 | `lyds_insert` | `lyd_new_inner -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 344 | `lyds_insert` | `lyd_new_list -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 345 | `lyds_insert` | `lyd_new_list2 -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 346 | `lyds_insert` | `lyd_new_list3 -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 347 | `lyds_insert` | `lyd_new_opaq -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 348 | `lyds_insert` | `lyd_new_opaq2 -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 349 | `lyds_insert` | `lyd_new_path_create -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 350 | `lyds_insert` | `lyd_new_term_raw -> lyd_insert_node -> lyds_insert (api)` | reaches; includes CHA-only candidate(s) | I/P |
| 351 | `lyds_insert` | `lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 352 | `lyds_insert` | `lydxml_env_netconf_reply -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 353 | `lyds_insert` | `lydxml_env_netconf_rpc -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 354 | `lyds_insert` | `ly_path_eval_partial -> lyd_create_list -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 355 | `lyds_insert` | `lyb_finish_node -> lyb_insert_node -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 356 | `lyds_insert` | `lyb_finish_opaq -> lyb_insert_node -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 357 | `lyds_insert` | `lyd_change_term_val -> lyd_change_node_value -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 358 | `lyds_insert` | `lyd_create_list2 -> lyd_create_list -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 359 | `lyds_insert` | `lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 360 | `lyds_insert` | `lyd_diff_merge_r -> lyd_diff_insert_sibling -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 361 | `lyds_insert` | `lyd_dup -> lyd_dup_get_local_parent -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 362 | `lyds_insert` | `lyd_merge -> lyd_merge_sibling_r -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 363 | `lyds_insert` | `lyd_new_implicit_module -> lyd_new_implicit -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 364 | `lyds_insert` | `lyd_new_implicit_r -> lyd_new_implicit -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 365 | `lyds_insert` | `lyd_new_implicit_tree -> lyd_new_implicit -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 366 | `lyds_insert` | `lyd_new_path_ -> lyd_new_path_create -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 367 | `lyds_insert` | `lyd_new_term -> _lyd_new_term -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 368 | `lyds_insert` | `lyd_parse_value_fragment -> lyd_new_path_create -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 369 | `lyds_insert` | `lyd_parse_xml_netconf -> lydxml_env_netconf_reply -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 370 | `lyds_insert` | `lyd_validate -> lyd_new_implicit -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 371 | `lyds_insert` | `lyd_validate_op -> _lyd_validate_op -> lyd_insert_node -> lyds_insert (api)` | reaches; includes CHA-only candidate(s) | I/P |
| 372 | `lyds_insert` | `lyd_validate_tree -> lyd_new_implicit -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 373 | `lyds_insert` | `lydjson_parse_any -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 374 | `lyds_insert` | `lydjson_parse_instance_inner -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 375 | `lyds_insert` | `lydjson_parse_instance_term -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 376 | `lyds_insert` | `lydjson_parse_opaq -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 377 | `lyds_insert` | `lydjson_subtree_r -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 378 | `lyds_insert` | `lydxml_subtree_any -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 379 | `lyds_insert` | `lydxml_subtree_inner -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 380 | `lyds_insert` | `lydxml_subtree_opaq -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 381 | `lyds_insert` | `lydxml_subtree_r -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 382 | `lyds_insert` | `lydxml_subtree_term -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 383 | `lyds_insert` | `moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 384 | `lyds_insert` | `_lyd_change_term -> lyd_change_term_val -> lyd_change_node_value -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 385 | `lyds_insert` | `eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 386 | `lyds_insert` | `ly_path_eval -> ly_path_eval_partial -> lyd_create_list -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 387 | `lyds_insert` | `lyb_parse_node_any -> lyb_finish_node -> lyb_insert_node -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 388 | `lyds_insert` | `lyb_parse_node_inner -> lyb_finish_node -> lyb_insert_node -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 389 | `lyds_insert` | `lyb_parse_node_leaf -> lyb_finish_node -> lyb_insert_node -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 390 | `lyds_insert` | `lyb_parse_node_list -> lyb_finish_node -> lyb_insert_node -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 391 | `lyds_insert` | `lyb_parse_node_opaq -> lyb_finish_opaq -> lyb_insert_node -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 392 | `lyds_insert` | `lyd_diff_merge_module -> lyd_diff_merge_r -> lyd_diff_insert_sibling -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 393 | `lyds_insert` | `lyd_diff_merge_tree -> lyd_diff_merge_r -> lyd_diff_insert_sibling -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 394 | `lyds_insert` | `lyd_diff_siblings_r -> lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 395 | `lyds_insert` | `lyd_dup_siblings -> lyd_dup -> lyd_dup_get_local_parent -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 396 | `lyds_insert` | `lyd_dup_siblings_to_ctx -> lyd_dup -> lyd_dup_get_local_parent -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 397 | `lyds_insert` | `lyd_dup_single -> lyd_dup -> lyd_dup_get_local_parent -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 398 | `lyds_insert` | `lyd_dup_single_to_ctx -> lyd_dup -> lyd_dup_get_local_parent -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 399 | `lyds_insert` | `lyd_find_path -> ly_path_eval_partial -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 400 | `lyds_insert` | `lyd_find_sibling_val -> lyd_create_list2 -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 401 | `lyds_insert` | `lyd_merge_module -> lyd_merge -> lyd_merge_sibling_r -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 402 | `lyds_insert` | `lyd_merge_siblings -> lyd_merge -> lyd_merge_sibling_r -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 403 | `lyds_insert` | `lyd_merge_tree -> lyd_merge -> lyd_merge_sibling_r -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 404 | `lyds_insert` | `lyd_new_path -> lyd_new_path_ -> lyd_new_path_create -> lyd_insert_node -> lyds_insert (api)` | reaches; includes CHA-only candidate(s) | I/P |
| 405 | `lyds_insert` | `lyd_new_path2 -> lyd_new_path_ -> lyd_new_path_create -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 406 | `lyds_insert` | `lyd_parse -> lyd_validate -> lyd_new_implicit -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 407 | `lyds_insert` | `lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 408 | `lyds_insert` | `lyd_parse_json_restconf -> lydjson_subtree_r -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 409 | `lyds_insert` | `lyd_parse_op -> lyd_parse_xml_netconf -> lydxml_env_netconf_reply -> lyd_insert_node -> lyds_insert (api)` | reaches; includes CHA-only candidate(s) | I/P |
| 410 | `lyds_insert` | `lyd_parse_xml -> lydxml_subtree_r -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 411 | `lyds_insert` | `lyd_parser_validate_new_implicit -> lyd_new_implicit_r -> lyd_new_implicit -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 412 | `lyds_insert` | `lyd_val_diff_add -> lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 413 | `lyds_insert` | `lyd_validate_all -> lyd_validate -> lyd_new_implicit -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 414 | `lyds_insert` | `lyd_validate_ext -> lyd_validate_tree -> lyd_new_implicit -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 415 | `lyds_insert` | `lyd_validate_module -> lyd_validate -> lyd_new_implicit -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 416 | `lyds_insert` | `lydjson_ctx_next_parse_opaq -> lydjson_parse_opaq -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 417 | `lyds_insert` | `lydjson_parse_instance -> lydjson_parse_any -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 418 | `lyds_insert` | `eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 419 | `lyds_insert` | `lyb_parse_node -> lyb_parse_node_any -> lyb_finish_node -> lyb_insert_node -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 420 | `lyds_insert` | `lyb_parse_node_leaflist -> lyb_parse_node_leaf -> lyb_finish_node -> lyb_insert_node -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 421 | `lyds_insert` | `lyb_validate_node_inner -> lyd_parser_validate_new_implicit -> lyd_new_implicit_r -> lyd_new_implicit -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 422 | `lyds_insert` | `lyd_change_term -> _lyd_change_term -> lyd_change_term_val -> lyd_change_node_value -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 423 | `lyds_insert` | `lyd_change_term_canon -> _lyd_change_term -> lyd_change_term_val -> lyd_change_node_value -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 424 | `lyds_insert` | `lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 425 | `lyds_insert` | `lyd_diff -> lyd_diff_siblings_r -> lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 426 | `lyds_insert` | `lyd_find_target -> ly_path_eval -> ly_path_eval_partial -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 427 | `lyds_insert` | `lyd_new_path_update -> _lyd_change_term -> lyd_change_term_val -> lyd_change_node_value -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 428 | `lyds_insert` | `lyd_parse_data -> lyd_parse -> lyd_validate -> lyd_new_implicit -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 429 | `lyds_insert` | `lyd_parse_json_bare_value -> lydjson_parse_instance -> lydjson_parse_any -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 430 | `lyds_insert` | `lyd_validate_autodel_node_del -> lyd_val_diff_add -> lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 431 | `lyds_insert` | `lyd_validate_cases -> lyd_val_diff_add -> lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 432 | `lyds_insert` | `lydjson_parse_attribute -> lydjson_ctx_next_parse_opaq -> lydjson_parse_opaq -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 433 | `lyds_insert` | `lyplg_type_validate_tree_instanceid -> ly_path_eval -> ly_path_eval_partial -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 434 | `lyds_insert` | `xpath_deref_type -> ly_path_eval -> ly_path_eval_partial -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 435 | `lyds_insert` | `eval_absolute_location_path -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 436 | `lyds_insert` | `eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 437 | `lyds_insert` | `lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyb_finish_node -> lyb_insert_node -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 438 | `lyds_insert` | `lyd_any_copy_value -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 439 | `lyds_insert` | `lyd_create_any -> lyd_create_any_datatree -> lyd_parse_json -> lydjson_subtree_r -> lyd_parser_node_insert -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 440 | `lyds_insert` | `lyd_diff_siblings -> lyd_diff -> lyd_diff_siblings_r -> lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 441 | `lyds_insert` | `lyd_diff_tree -> lyd_diff -> lyd_diff_siblings_r -> lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 442 | `lyds_insert` | `lyd_validate_autodel_case_dflt -> lyd_validate_autodel_node_del -> lyd_val_diff_add -> lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 443 | `lyds_insert` | `lyd_validate_autodel_cont_leaf_dflt -> lyd_validate_autodel_node_del -> lyd_val_diff_add -> lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 444 | `lyds_insert` | `lyd_validate_autodel_leaflist_dflt -> lyd_validate_autodel_node_del -> lyd_val_diff_add -> lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 445 | `lyds_insert` | `lyd_validate_choice_r -> lyd_validate_cases -> lyd_val_diff_add -> lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 446 | `lyds_insert` | `lyd_validate_unres -> lyplg_type_validate_tree_instanceid -> ly_path_eval -> ly_path_eval_partial -> lyd_create_list -> lyd_insert_node -> lyds_insert (fp-model)` | reaches; includes CHA-only candidate(s) | I/P |
| 447 | `lyds_insert` | `lyd_validate_unres_when -> lyd_validate_autodel_node_del -> lyd_val_diff_add -> lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 448 | `lyds_insert` | `xpath_deref -> xpath_deref_type -> ly_path_eval -> ly_path_eval_partial -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 449 | `lyds_insert` | `eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 450 | `lyds_insert` | `lyd_parse_lyb -> lyb_parse_siblings -> lyb_parse_node -> lyb_parse_node_any -> lyb_finish_node -> lyb_insert_node -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 451 | `lyds_insert` | `lyd_validate_new -> lyd_validate_autodel_case_dflt -> lyd_validate_autodel_node_del -> lyd_val_diff_add -> lyd_diff_add -> lyd_diff_dup -> lyd_insert_node -> lyds_insert` | reaches; includes CHA-only candidate(s) | I/P |
| 452 | `lyds_insert` | `eval_additive_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 453 | `lyds_insert` | `eval_and_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 454 | `lyds_insert` | `eval_equality_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 455 | `lyds_insert` | `eval_function_call -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 456 | `lyds_insert` | `eval_multiplicative_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 457 | `lyds_insert` | `eval_or_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 458 | `lyds_insert` | `eval_predicate -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 459 | `lyds_insert` | `eval_relational_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 460 | `lyds_insert` | `eval_unary_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 461 | `lyds_insert` | `eval_union_expr -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 462 | `lyds_insert` | `eval_variable_reference -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 463 | `lyds_insert` | `lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 464 | `lyds_insert` | `lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 465 | `lyds_insert` | `eval_name_test_try_compile_predicate_append -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 466 | `lyds_insert` | `eval_node_type_with_predicate -> eval_predicate -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 467 | `lyds_insert` | `lyd_eval_xpath4 -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 468 | `lyds_insert` | `lyd_trim_xpath -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 469 | `lyds_insert` | `lyd_validate_must -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 470 | `lyds_insert` | `lyd_validate_node_when -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 471 | `lyds_insert` | `lyplg_type_resolve_leafref -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 472 | `lyds_insert` | `lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 473 | `lyds_insert` | `lys_compile_unres_when -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 474 | `lyds_insert` | `lys_compile_unres_when_cyclic -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 475 | `lyds_insert` | `lys_find_expr_atoms -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 476 | `lyds_insert` | `lys_find_xpath -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 477 | `lyds_insert` | `lys_find_xpath_atoms -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 478 | `lyds_insert` | `eval_name_test_try_compile_predicates -> eval_name_test_try_compile_predicate_append -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 479 | `lyds_insert` | `lyd_validate_dummy_when -> lyd_validate_node_when -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 480 | `lyds_insert` | `lyd_validate_final_r -> lyd_validate_must -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 481 | `lyds_insert` | `lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 482 | `lyds_insert` | `lyd_validate_mandatory -> lyd_validate_dummy_when -> lyd_validate_node_when -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 483 | `lyds_insert` | `lyd_validate_minmax -> lyd_validate_dummy_when -> lyd_validate_node_when -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 484 | `lyds_insert` | `lyd_validate_module_final -> lyd_validate_final_r -> lyd_validate_must -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 485 | `lyds_insert` | `lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 486 | `lyds_insert` | `lyd_validate_siblings_schema_r -> lyd_validate_mandatory -> lyd_validate_dummy_when -> lyd_validate_node_when -> lyxp_eval -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | reaches | I/P |
| 487 | `lyds_insert` | `lys_compile_depset_all -> lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 488 | `lyds_insert` | `ly_ctx_compile -> lys_compile_depset_all -> lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 489 | `lyds_insert` | `ly_ctx_load_module -> lys_compile_depset_all -> lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | reaches | I/P |
| 490 | `lyds_insert` | `lys_parse -> lys_compile_depset_all -> lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 491 | `lyds_insert` | `lys_set_implemented -> lys_compile_depset_all -> lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert (api)` | no-go-mapping | I/P |
| 492 | `lyds_insert` | `lys_unres_glob_revert -> lys_compile_depset_all -> lys_compile_depset_r -> lys_compile_unres_depset -> lys_compile_unres_must -> lyxp_atomize -> eval_expr_select -> eval_path_expr -> eval_relative_location_path -> eval_name_test_with_predicate -> moveto_node_hash_child -> lyd_create_list -> lyd_insert_node -> lyds_insert` | no-go-mapping | I/P |
| 493 | `lyds_split` | `lyd_unlink_siblings -> lyds_split (api)` | no-go-mapping | excluded: U-0024 schema mount |
| 494 | `lyds_merge_nodes1` | `lyds_merge -> lyds_merge_nodes1` | reaches; includes CHA-only candidate(s) | M |
| 495 | `lyds_merge_nodes1` | `lyds_merge_nodes2 -> lyds_merge_nodes1` | reaches; includes CHA-only candidate(s) | M |
| 496 | `lyds_merge_nodes1` | `lyds_merge_nodes3 -> lyds_merge_nodes1` | reaches; includes CHA-only candidate(s) | M |
| 497 | `lyds_merge_nodes1` | `lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes1` | reaches; includes CHA-only candidate(s) | M |
| 498 | `lyds_merge_nodes1` | `lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes1` | reaches; includes CHA-only candidate(s) | M |
| 499 | `lyds_merge_nodes1` | `lyd_insert_child -> lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes1 (api)` | reaches | M |
| 500 | `lyds_merge_nodes1` | `lyd_insert_sibling -> lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes1 (api)` | reaches | M |
| 501 | `lyds_merge_nodes2` | `lyds_merge -> lyds_merge_nodes2` | reaches; includes CHA-only candidate(s) | M |
| 502 | `lyds_merge_nodes2` | `lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2` | reaches; includes CHA-only candidate(s) | M |
| 503 | `lyds_merge_nodes2` | `lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2` | reaches; includes CHA-only candidate(s) | M |
| 504 | `lyds_merge_nodes2` | `lyd_insert_child -> lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 (api)` | reaches | M |
| 505 | `lyds_merge_nodes2` | `lyd_insert_sibling -> lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 (api)` | reaches | M |
| 506 | `lyds_merge_nodes2_among` | `lyds_merge_nodes2 -> lyds_merge_nodes2_among` | reaches; includes CHA-only candidate(s) | M |
| 507 | `lyds_merge_nodes2_among` | `lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_among` | reaches; includes CHA-only candidate(s) | M |
| 508 | `lyds_merge_nodes2_among` | `lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_among` | reaches; includes CHA-only candidate(s) | M |
| 509 | `lyds_merge_nodes2_among` | `lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_among` | reaches; includes CHA-only candidate(s) | M |
| 510 | `lyds_merge_nodes2_among` | `lyd_insert_child -> lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_among (api)` | reaches | M |
| 511 | `lyds_merge_nodes2_among` | `lyd_insert_sibling -> lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_among (api)` | reaches | M |
| 512 | `lyds_merge_nodes2_back` | `lyds_merge_nodes2 -> lyds_merge_nodes2_back` | reaches; includes CHA-only candidate(s) | M |
| 513 | `lyds_merge_nodes2_back` | `lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_back` | reaches; includes CHA-only candidate(s) | M |
| 514 | `lyds_merge_nodes2_back` | `lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_back` | reaches; includes CHA-only candidate(s) | M |
| 515 | `lyds_merge_nodes2_back` | `lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_back` | reaches; includes CHA-only candidate(s) | M |
| 516 | `lyds_merge_nodes2_back` | `lyd_insert_child -> lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_back (api)` | reaches | M |
| 517 | `lyds_merge_nodes2_back` | `lyd_insert_sibling -> lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_back (api)` | reaches | M |
| 518 | `lyds_merge_nodes2_front` | `lyds_merge_nodes2 -> lyds_merge_nodes2_front` | reaches; includes CHA-only candidate(s) | M |
| 519 | `lyds_merge_nodes2_front` | `lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_front` | reaches; includes CHA-only candidate(s) | M |
| 520 | `lyds_merge_nodes2_front` | `lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_front` | reaches; includes CHA-only candidate(s) | M |
| 521 | `lyds_merge_nodes2_front` | `lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_front` | reaches; includes CHA-only candidate(s) | M |
| 522 | `lyds_merge_nodes2_front` | `lyd_insert_child -> lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_front (api)` | reaches | M |
| 523 | `lyds_merge_nodes2_front` | `lyd_insert_sibling -> lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes2 -> lyds_merge_nodes2_front (api)` | reaches | M |
| 524 | `lyds_merge_nodes3` | `lyds_merge -> lyds_merge_nodes3` | reaches; includes CHA-only candidate(s) | M |
| 525 | `lyds_merge_nodes3` | `lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes3` | reaches; includes CHA-only candidate(s) | M |
| 526 | `lyds_merge_nodes3` | `lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes3` | reaches; includes CHA-only candidate(s) | M |
| 527 | `lyds_merge_nodes3` | `lyd_insert_child -> lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes3 (api)` | reaches | M |
| 528 | `lyds_merge_nodes3` | `lyd_insert_sibling -> lyd_move_nodes -> lyd_move_nodes_by_schema -> lyds_merge -> lyds_merge_nodes3 (api)` | reaches | M |

All 528 caller rows are included.

## Go mutation sites

- [UNCOVERED] Node.copyAnyValue @ data/any.go:383:16 fields=['nodeDeque.list'] via=[] C=['lyd_any_copy_value', 'lyd_create_any_datatree'] mentioned=False  chain=Node.copyAnyValue -> anyWork.createAny -> xmlParser.subtreeAny -> xmlParser.subtree -> parseXML -> parseWith -> Parse writes=['data/any.go:400:21']
- [TABLE] Node.takeAnyChildren @ data/any.go:333:16 fields=['nodeDeque.list', 'siblings.*'] via=[] C=[] mentioned=True  chain= writes=['data/any.go:338:19', 'data/any.go:349:20']
- [UNCOVERED] PrintOptions.isDefault @ data/print.go:81:23 fields=['nodeDeque.list'] via=[] C=['lyd_is_default', 'lyd_value_validate3'] mentioned=False  chain=PrintOptions.isDefault -> PrintOptions.shouldPrint -> printer.shouldPrint -> jsonPrinter.node -> printJSON -> runPrint -> printData -> Tree.PrintJSON writes=['data/print.go:109:13']
- [behind-table] Tree.buildRBOrder @ data/insert.go:518:16 fields=['siblings.rbOrders'] via=['dirtyOrder.insertCompatible'] C=['lyds_additionally_create_rb_tree', 'lyds_insert', 'lyds_is_supported', 'rb_compare_leaflists', 'rb_compare_lists'] mentioned=False  chain= writes=['data/insert.go:527:7', 'data/insert.go:529:14']
- [UNCOVERED] Tree.created @ data/edit.go:993:16 fields=[] via=['nodeDeque.insertAt'] C=['lyd_find_sibling_dup_inst_set'] mentioned=False  chain=Tree.created -> Tree.applyR -> Tree.applyDiffWithModule -> Tree.ApplyDiff writes=[]
- [TABLE] Tree.flushMerge @ data/edit.go:1267:16 fields=[] via=['cloneDirtyOrder'] C=[] mentioned=True  chain= writes=[]
- [TABLE] Tree.linkOrigin @ data/move.go:327:16 fields=['Node.inRB', 'Node.rbGroup', 'siblings.rbOrders', 'siblings.rbTree', 'siblings.unsorted'] via=['dirtyOrder.armRemovalUndo', 'dirtyOrder.removalBound'] C=[] mentioned=True  chain= writes=['data/move.go:395:9', 'data/move.go:398:16', 'data/move.go:405:6', 'data/move.go:406:6', 'data/move.go:413:11', 'data/move.go:418:11']
- [TABLE] Tree.merge @ data/move.go:772:16 fields=['Node.inRB', 'Node.rbGroup'] via=['cloneDirtyOrder', 'dirtyOrder.ordered', 'dirtyOrder.postOrder'] C=['lyd_merge', 'lyds_additionally_create_rb_tree', 'lyds_merge', 'lyds_merge_nodes1', 'lyds_merge_nodes2'] mentioned=True  chain= writes=['data/move.go:860:13', 'data/move.go:860:5']
- [TABLE] Tree.moveNodes @ data/move.go:608:16 fields=['Node.inRB', 'Node.rbGroup', 'siblings.rbTree', 'siblings.unsorted'] via=[] C=['lyd_move_nodes', 'lyd_move_nodes_at_once', 'lyd_move_nodes_by_schema', 'lyd_move_nodes_ordby_schema'] mentioned=True  chain= writes=['data/move.go:661:6', 'data/move.go:662:6', 'data/move.go:673:11', 'data/move.go:678:11']
- [UNCOVERED] Tree.moved @ data/edit.go:1031:16 fields=[] via=['nodeDeque.deleteAt'] C=['lyd_diff_insert'] mentioned=False  chain=Tree.moved -> Tree.applyR -> Tree.applyDiffWithModule -> Tree.ApplyDiff writes=[]
- [UNCOVERED] Tree.rbAdd @ data/insert.go:586:16 fields=[] via=['dirtyOrder.place', 'dirtyOrder.search'] C=['rb_insert'] mentioned=False  chain=Tree.rbAdd -> Tree.sortedStorageRefusal -> Tree.preflightSortedStorage -> Tree.preflightInsertPosition -> Tree.dupPublic -> Tree.Dup writes=[]
- [behind-table] Tree.rbInsert @ data/insert.go:534:16 fields=[] via=['dirtyOrder.insertCompatible'] C=['lyds_additionally_create_rb_tree', 'lyds_insert', 'lyds_is_supported', 'rb_compare_leaflists', 'rb_compare_lists'] mentioned=False  chain= writes=[]
- [behind-table] Tree.rbPos @ data/insert.go:606:16 fields=['Node.inRB', 'Node.rbGroup', 'nodeDeque.list', 'siblings.rbOrders'] via=['dirtyOrder.ordered', 'dirtyOrder.place', 'dirtyOrder.search'] C=['lyds_additionally_create_rb_tree', 'lyds_insert', 'lyds_is_supported', 'lyds_link_data_node', 'rb_compare_leaflists'] mentioned=False  chain= writes=['data/insert.go:653:7', 'data/insert.go:660:6', 'data/insert.go:671:8', 'data/insert.go:673:15', 'data/insert.go:692:4', 'data/insert.go:693:4']
- [behind-table] Tree.rbPublish @ data/insert.go:1980:16 fields=['Node.inRB', 'Node.rbGroup', 'nodeDeque.base', 'nodeDeque.list', 'siblings.rbOrders'] via=['dirtyOrder.ordered'] C=['lyd_insert_hash', 'lyd_insert_hash_add', 'lyds_link_data_node', 'lyds_merge', 'lyds_merge_nodes1'] mentioned=False  chain= writes=['data/insert.go:2009:16', 'data/insert.go:2009:6', 'data/insert.go:2012:13', 'data/insert.go:2012:5', 'data/insert.go:2015:7', 'data/insert.go:2018:14', 'data/insert.go:2055:8', 'data/insert.go:2058:7']
- [behind-table] Tree.snapshotRB @ data/move.go:561:16 fields=[] via=['cloneDirtyOrder'] C=[] mentioned=False  chain= writes=[]
- [TABLE] Tree.unlinkAllDefaults @ data/insert.go:2292:16 fields=['Node.inRB', 'Node.rbGroup', 'nodeDeque.list'] via=[] C=[] mentioned=True  chain= writes=['data/insert.go:2392:7', 'data/insert.go:2453:24', 'data/insert.go:2453:32']
- [TABLE] detach @ data/insert.go:2220:6 fields=['Node.inRB', 'Node.rbGroup'] via=[] C=[] mentioned=True  chain= writes=['data/insert.go:2223:22', 'data/insert.go:2223:30']
- [behind-table] dupCache.added @ data/edit.go:972:20 fields=['nodeDeque.list'] via=[] C=[] mentioned=False  chain= writes=['data/edit.go:984:11']
- [TABLE] give @ data/move.go:529:6 fields=['siblings.rbOrders'] via=[] C=['lyd_move_nodes', 'lyd_move_nodes_at_once', 'lyd_move_nodes_by_schema', 'lyd_move_nodes_ordby_schema', 'lyds_tree'] mentioned=True  chain= writes=['data/move.go:535:9', 'data/move.go:538:16']
- [UNCOVERED] hashTransaction.captureSiblings @ data/hash_transaction.go:314:27 fields=[] via=['cloneDirtyOrder'] C=[] mentioned=False  chain=hashTransaction.captureSiblings -> siblings.touch -> siblings.addContext -> sibsOf -> Node.print -> Node.PrintJSON writes=[]
- [TABLE] hashTransaction.restore @ data/hash_transaction.go:396:27 fields=['dirtyOrder.*', 'dirtyOrderNode.*', 'siblings.*'] via=[] C=[] mentioned=True  chain= writes=['data/hash_transaction.go:411:3', 'data/hash_transaction.go:427:3', 'data/hash_transaction.go:430:3']
- [TABLE] lydCtx.appendSorted @ data/parse.go:780:19 fields=['Node.inRB', 'Node.rbGroup'] via=['dirtyOrder.first', 'dirtyOrder.insert', 'dirtySize', 'newDirtyOrder'] C=['lyd_parser_check_keys', 'lyd_parser_check_schema', 'lyd_parser_create_term', 'lyd_parser_node_free', 'lyd_parser_node_insert'] mentioned=True parser rows (category: sort/reorder helpers, repeated-key parsing) chain= writes=['data/parse.go:839:9', 'data/parse.go:859:4', 'data/parse.go:860:4']
- [TABLE] lydCtx.applyLeaders @ data/parse.go:887:19 fields=['dirtyOrder.leaders'] via=[] C=['lyd_insert_hash_add', 'lyds_insert'] mentioned=True parser rows (category: sort/reorder helpers, repeated-key parsing) chain= writes=['data/parse.go:898:6']
- [TABLE] lydCtx.flushDirtyRun @ data/parse.go:1013:19 fields=['lydCtx.dirtyOrd', 'nodeDeque.list', 'siblings.pending'] via=['dirtyOrder.ordered'] C=['lyds_additionally_create_rb_nodes'] mentioned=True parser rows (category: sort/reorder helpers, repeated-key parsing) chain= writes=['data/parse.go:1016:9', 'data/parse.go:1017:9', 'data/parse.go:1054:6']
- [behind-table] lydCtx.markDirty @ data/parse.go:901:19 fields=['lydCtx.dirtyOrd', 'siblings.pending'] via=[] C=[] mentioned=False parser rows (category: sort/reorder helpers, repeated-key parsing) chain= writes=['data/parse.go:916:6', 'data/parse.go:918:13', 'data/parse.go:920:7', 'data/parse.go:922:13']
- [TABLE] lydCtx.nodeFree @ data/parse.go:1095:19 fields=[] via=['dirtyOrder.remove'] C=['lyd_parser_check_keys', 'lyd_parser_check_schema', 'lyd_parser_create_term', 'lyd_parser_node_free', 'lyd_parser_node_insert'] mentioned=True parser rows (category: sort/reorder helpers, repeated-key parsing) chain= writes=[]
- [TABLE] markRB @ data/insert.go:1407:6 fields=['Node.rbGroup', 'siblings.rbGroups', 'siblings.rbTree'] via=[] C=['lyds_create_metadata'] mentioned=True  chain= writes=['data/insert.go:1410:7', 'data/insert.go:1412:12', 'data/insert.go:1414:7', 'data/insert.go:1422:15', 'data/insert.go:1430:16']
- [behind-table] markUnsorted @ data/insert.go:1437:6 fields=['siblings.unsorted'] via=[] C=['lyds_additionally_create_rb_tree', 'lyds_insert', 'lyds_is_supported', 'rb_compare_leaflists', 'rb_compare_lists'] mentioned=False  chain= writes=['data/insert.go:1440:7', 'data/insert.go:1442:14']
- [behind-table] rbSnapshot.restore @ data/move.go:586:22 fields=['Node.inRB', 'Node.rbGroup', 'nodeDeque.base', 'nodeDeque.list', 'siblings.rbGroups', 'siblings.rbOrders', 'siblings.rbTree', 'siblings.unsorted'] via=[] C=[] mentioned=True  chain= writes=['data/move.go:590:17', 'data/move.go:590:7', 'data/move.go:592:20', 'data/move.go:592:32', 'data/move.go:592:46', 'data/move.go:593:6', 'data/move.go:599:13', 'data/move.go:599:5']
- [behind-table] siblings.clearEmptyRBGroup @ data/siblings.go:393:20 fields=['siblings.rbOrders'] via=[] C=['rb_remove_node'] mentioned=False  chain= writes=['data/siblings.go:411:8']
- [behind-table] siblings.insertAt @ data/insert.go:1873:20 fields=[] via=['nodeDeque.insertAt'] C=[] mentioned=False  chain= writes=[]
- [behind-table] siblings.opIdxAdd @ data/insert.go:1292:20 fields=[] via=['nodeDeque.insertAt'] C=[] mentioned=False  chain= writes=[]
- [behind-table] siblings.opRemove @ data/insert.go:1232:20 fields=[] via=['nodeDeque.deleteAt'] C=[] mentioned=False  chain= writes=[]
- [TABLE] siblings.removeRBNode @ data/siblings.go:416:20 fields=[] via=['dirtyOrder.remove'] C=['lyds_unlink', 'rb_remove_node'] mentioned=True  chain= writes=[]
- [behind-table] siblings.runGone @ data/siblings.go:384:20 fields=['siblings.rbGroups', 'siblings.rbTree', 'siblings.unsorted'] via=[] C=[] mentioned=False  chain= writes=['data/siblings.go:386:8', 'data/siblings.go:387:8', 'data/siblings.go:388:8']
- [TABLE] siblings.transferRun @ data/siblings.go:356:20 fields=['siblings.rbGroups', 'siblings.rbTree', 'siblings.unsorted'] via=[] C=[] mentioned=True  chain= writes=['data/siblings.go:365:12', 'data/siblings.go:367:15', 'data/siblings.go:372:9', 'data/siblings.go:374:10', 'data/siblings.go:378:13', 'data/siblings.go:379:9']
- [TABLE] take @ data/move.go:493:6 fields=['Node.inRB', 'Node.rbGroup', 'siblings.rbOrders'] via=[] C=['lyd_move_nodes', 'lyd_move_nodes_at_once', 'lyd_move_nodes_by_schema', 'lyd_move_nodes_ordby_schema', 'lyd_unlink_ignore_lyds'] mentioned=True  chain= writes=['data/move.go:501:10', 'data/move.go:513:13', 'data/move.go:515:20', 'data/move.go:521:5', 'data/move.go:522:5']
- [TABLE] unlink @ data/insert.go:2145:6 fields=[] via=['nodeDeque.deleteAt'] C=['lyd_del_move_root', 'lyd_unlink', 'lyd_unlink_check', 'lyd_unlink_ignore_lyds', 'lyd_unlink_tree'] mentioned=True  chain= writes=[]
- [UNCOVERED] xs.PrepareInstance @ data/xpathnode.go:441:13 fields=['nodeDeque.list'] via=[] C=['lyd_create_inner', 'lyd_create_list', 'lyd_create_term'] mentioned=False  chain=xs.PrepareInstance writes=['data/xpathnode.go:465:16']

All 39 mutation sites are included. Original line numbers describe the input snapshot.

`snapshotRB` / `rbSnapshot.restore` are replaced by J: one authoritative journal for both
forests. `rbInsert` is replaced by the charged `rbAdd`/`search`/`place` path; parser insertion
uses the retained order directly. `buildRBOrder` is charged test setup, not a production parser
constructor. `clearEmptyLeaders` and `dropDual` are retired: removal replays intermediate leader
changes, and unsafe retained dual-member detach is refused. `markDirty`/`applyLeaders`/publication
are P; `clearEmptyRBGroup`/`runGone`/removal are R/B; `insertAt`/ordered-index hooks are I;
`dupCache.added` is D. Index and printer/XPath temporary copies listed by the scanner do not own
retained topology. They are not a second parser tree.
