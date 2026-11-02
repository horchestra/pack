package dev.kstool.pack.sdk;

import java.lang.annotation.ElementType;
import java.lang.annotation.Retention;
import java.lang.annotation.RetentionPolicy;
import java.lang.annotation.Target;

/**
 * The tags of a Go struct field, as an annotation. A setting of a pack is a public field; without this annotation it is
 * a required property named like the field.
 */
@Retention(RetentionPolicy.RUNTIME)
@Target(ElementType.FIELD)
public @interface Setting {
    /** The property's name in the schema (the field's name by default): the application's own key. */
    String json() default "";

    /** The field is the environment variable NAME. */
    String env() default "";

    /** The field - a leaf or a whole subtree - becomes the file at this path in the container. */
    String file() default "";

    /** ... rendered from the pack's content/&lt;template&gt; (a *.ptmpl), or, with format "raw", shipped as it is. */
    String template() default "";

    /** ... or the subtree written in that format (json, yaml, properties, env, ini, toml, ...). */
    String format() default "";

    /** The file's mode, "0640". */
    String mode() default "";

    /** How a template's output is parsed back, where it is. */
    String parseAs() default "";

    /** The field is optional; without it the field is required (the Go json tag's omitempty). */
    boolean omitempty() default false;

    /** Not a setting (json:"-"). */
    boolean skip() default false;
}
