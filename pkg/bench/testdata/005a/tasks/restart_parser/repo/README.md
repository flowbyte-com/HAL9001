# Parser input rules

- `ParseInt` accepts an optional sign and surrounding whitespace.
- `ParseList` splits on commas, trims surrounding whitespace from each item,
  and drops empty items.
- An empty input list is nil.
