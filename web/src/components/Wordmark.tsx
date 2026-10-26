import mark from "../assets/kubexray-icon.svg";

/** The kubexray mark and wordmark. The wordmark is text, so it follows the
 * light and dark themes; the mark reads on both. */
export function Wordmark() {
  return (
    <h1 className="wordmark">
      <img className="wordmark-mark" src={mark} alt="" width={26} height={26} />
      <span>
        kube<span>xray</span>
      </span>
    </h1>
  );
}
