"""Run with python3 test_listener_contract.py /path/to/java (JDK 11+)."""

import os
import subprocess
import sys
import tempfile
from pathlib import Path

module = (
    Path(__file__).parent / "src/main/java/ao/mobile/imagepaste/ImagePasteModule.java"
)
source = module.read_text()
start = source.index("  @ReactMethod public void addListener(")
end = source.index("  @ReactMethod public void detach(", start)
methods = source[start:end].replace("@ReactMethod ", "")
checks = """
  public static void main(String[] args) {
    ListenerContractCheck module = new ListenerContractCheck();
    module.addListener("AOImagePaste");
    module.removeListeners(0);
    module.removeListeners(1);
    module.removeListeners(4);
    try {
      module.addListener(null);
      throw new AssertionError("null event name accepted");
    } catch (IllegalArgumentException expected) {}
    for (double count : new double[] {
        -1, 0.5, Double.NaN, Double.POSITIVE_INFINITY, Double.NEGATIVE_INFINITY}) {
      try {
        module.removeListeners(count);
        throw new AssertionError("invalid count accepted: " + count);
      } catch (IllegalArgumentException expected) {}
    }
    System.out.println("Native listener contract: valid and invalid inputs passed");
  }
}
"""
java = sys.argv[1] if len(sys.argv) > 1 else "java"
with tempfile.TemporaryDirectory() as directory:
    harness = Path(directory) / "ListenerContractCheck.java"
    harness.write_text("public class ListenerContractCheck {\n" + methods + checks)
    subprocess.run(
        [java, str(harness)],
        check=True,
        env={**os.environ, "TMPDIR": directory},
    )
