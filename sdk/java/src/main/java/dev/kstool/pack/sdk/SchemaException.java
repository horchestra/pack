package dev.kstool.pack.sdk;

/** A pack that cannot be turned into its artifacts. */
public class SchemaException extends RuntimeException {
    private static final long serialVersionUID = 1L;

    public SchemaException(String message) {
        super(message);
    }
}
