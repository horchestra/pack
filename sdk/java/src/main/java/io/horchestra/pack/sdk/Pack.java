package io.horchestra.pack.sdk;

import java.util.List;
import java.util.Map;

/**
 * What an author of a pack extends. The subclass's public fields are the application's settings (annotate them with
 * {@link Setting}); a field whose type is a pack holds that pack - leave it null and annotate it omitempty to make it
 * optional. The class needs a no-argument constructor.
 */
public abstract class Pack {
    /** What the zero values cannot say; sets, never adds (it may run twice). */
    public void setDefaults() {
    }

    /** The identity: name, version, image. */
    public abstract Meta meta();

    /** How it runs, upgrades and rolls back: ports, volumes, readiness, hooks, savepoints ... */
    public abstract Root packRoot();

    /** Optional: {pointer: CEL formula} that fills a value. The keys come from {@link Paths#pointer}. */
    public Map<String, String> packCompute() {
        return Map.of();
    }

    /** Optional: {pointer: rules} that the values must satisfy. */
    public Map<String, List<Rule>> packRules() {
        return Map.of();
    }

    /** Optional: the operations the author names, pack run performs (x-pack-actions). */
    public List<Action> packActions() {
        return List.of();
    }

    /** Optional: what failures of the pack look like and the ways out of them (pack diagnose). */
    public List<Diagnosis> packDiagnose() {
        return List.of();
    }

    /** Optional: {pointer: suggestion} that pack fill offers for a setting not given. */
    public Map<String, Suggest> packSuggest() {
        return Map.of();
    }
}
