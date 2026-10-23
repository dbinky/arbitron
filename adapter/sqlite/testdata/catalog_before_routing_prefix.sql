-- State written by 0ce2359's migration-022 catalog schema and identity mapper.
-- recognise only stripped lab aliases, so a provider's own routing prefix
-- produced provider/native rather than deepseek/deepseek-v4-flash.
INSERT INTO model_catalog_state VALUES (1, '2026-10-01T00:00:00Z', '[]');
INSERT INTO model_catalog_models VALUES (
 'openrouter', 'openrouter/deepseek-v4-flash', '', '2026-10-01', 'https://models.dev/api.json', 1,
 '{"CanonicalID":"openrouter/openrouter/deepseek-v4-flash","Provider":"openrouter","NativeID":"openrouter/deepseek-v4-flash","Tier":"flash","OrderKey":"4","ReleaseDate":"2026-10-01","PolicyRevision":1}'
);
INSERT INTO model_catalog_slices VALUES ('openrouter',
 '{"Provider":"openrouter","Models":[{"CatalogProvider":"openrouter","NativeID":"openrouter/deepseek-v4-flash","ReleaseDate":"2026-10-01","SourceRef":"https://models.dev/api.json"}],"Entries":[{"CanonicalID":"openrouter/openrouter/deepseek-v4-flash","Provider":"openrouter","NativeID":"openrouter/deepseek-v4-flash","Tier":"flash","OrderKey":"4","ReleaseDate":"2026-10-01","PolicyRevision":1}],"Status":"ok"}'
);
INSERT INTO model_usability_evidence(canonical_id,path_key,config_revision,native_model,checked_at,proof_ref,state) VALUES (
 'openrouter/openrouter/deepseek-v4-flash', 'legacy-path', 'legacy-config',
 'openrouter/deepseek-v4-flash', '2026-10-01T00:00:00Z', 'legacy-proof', 'confirmed'
);
INSERT INTO model_first_observations VALUES (
 'openrouter/openrouter/deepseek-v4-flash',
 '{"Entry":{"CanonicalID":"openrouter/openrouter/deepseek-v4-flash","Provider":"openrouter","NativeID":"openrouter/deepseek-v4-flash","Tier":"flash","OrderKey":"4","PolicyRevision":1},"Paths":[{"CanonicalID":"openrouter/openrouter/deepseek-v4-flash","PathKey":"legacy-path","ConfigRevision":"legacy-config","NativeModel":"openrouter/deepseek-v4-flash","CheckedAt":"2026-10-01T00:00:00Z","ProofRef":"legacy-proof","State":"confirmed"}],"FirstConfirmedAt":"2026-10-01T00:00:00Z"}'
);
INSERT INTO model_announcement_events(id,canonical_id,payload,state) VALUES (
 'legacy-event', 'openrouter/openrouter/deepseek-v4-flash',
 '{"EventID":"legacy-event","CanonicalID":"openrouter/openrouter/deepseek-v4-flash","Provider":"openrouter","Tier":"flash","FirstConfirmedAt":"2026-10-01T00:00:00Z","Paths":[{"CanonicalID":"openrouter/openrouter/deepseek-v4-flash","PathKey":"legacy-path","ConfigRevision":"legacy-config","NativeModel":"openrouter/deepseek-v4-flash","CheckedAt":"2026-10-01T00:00:00Z","ProofRef":"legacy-proof","State":"confirmed"}],"Changes":[]}',
 'suppressed'
);
