import { createContext } from "react";
import type { Set as ChangeSet } from "../gen/feed";

/** The change feed, for parts of the page that show a change's diff next
 * to something else: a cause on an incident card. Empty where there's no
 * feed. */
export const FeedSets = createContext<ChangeSet[]>([]);
