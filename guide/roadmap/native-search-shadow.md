# Native Search shadow and invocation view

The optional native Search adapter observes the same cleaned candidate lanes as the existing baseline. Default configuration is off. Shadow mode preserves the baseline ranking, packing, final transaction and usage behavior; arithmetic results are diagnostics, not the returned policy.

The adapter reads native state through the existing authenticated service actor and source permissions. Unsupported actors, features, history policies, candidate bounds and unavailable state produce an explicit fallback. It does not create grants or treat a missing feature as zero. A native read has its own validated publication identity and clock; it does not establish the earliest durable availability of that state.

`BuildContext` accepts an optional `context_view` using `context-view-exclusion-v1`. Its exact four-part scope and memory reference/revision exclusions filter fresh cleaned lanes, history projection and resolved pins before packing. Search retrieval is not repeated and surviving lane order is preserved. Missing or null views preserve the previous wire behavior. The public Search input has no new field.

This view is a selector for one invocation. It does not delete records, erase prior delivery, change another call or persist into a new session. Persistent correction and withdrawal reuse the existing capture, human review and deletion APIs with their original permissions. Existing `Hit.created_by` is also documented as an optional field; its runtime behavior is unchanged.

The original observation protocol keeps `SelectedRefs` as the full arithmetic ranking. The separate `sys-native-service-stages-v2` record distinguishes full ranking, prepack, arithmetic emission, observed baseline before the final transaction and actual returned baseline. A rejected reference identifies the checked reference; an unknown underlying cause remains null. Transaction errors are not attributed to an invented source cause.

Server emission is not reader delivery. Reader acknowledgement, actual use, task success and causal attribution remain unknown in these records. The production host/crew receipt chain and its combined invocation-view checks are separate acceptance work. There is no UI change, production enablement, build or deployment in this stage.

Local SYS-01/SYS-03/SYS-04 integration is now accepted in the pinned latest-main candidate; see [delivery and lifecycle](native-delivery-lifecycle.md) for exact reader, recovery, trust and test boundaries. Default feature enablement and deployment remain separate.
