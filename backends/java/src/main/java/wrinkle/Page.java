package wrinkle;

/** The bound of responses that list things, so every frame from the server stays within 1 MiB. */
public final class Page {
  private Page() {}

  /**
   * How much a page may hold before it ends early; with the largest single item (a message of 48
   * KiB with its reactions) on top, the frame stays well within 1 MiB.
   */
  public static final int MAX_BYTES = 512 << 10;
}
