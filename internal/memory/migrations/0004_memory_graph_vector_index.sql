-- The memory map links each memory to its nearest neighbours (#175); comparing every pair took 12.8 s
-- at 5,202 memories. This index is on a halfvec expression only that query uses: recall orders by the
-- vector column itself, so its exact search plan does not change.
CREATE INDEX memory_records_memory_graph_vector ON arkism.memory_records
USING hnsw ((embedding::extensions.halfvec(1024)) extensions.halfvec_cosine_ops) WHERE collection='memories';
