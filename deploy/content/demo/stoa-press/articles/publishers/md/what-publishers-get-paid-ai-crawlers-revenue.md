# What a publisher earned from AI crawlers

For most of its life Stoa Press earned nothing from machines. Crawlers read
the essays, the essays turned up inside answers somewhere else, and no invoice
was ever raised. This article walks through one imagined month after the
catalogue was put behind FORA offers, so that a reader can see what the
numbers look like and where each one comes from.

## The month in one table

| Line | Count | Unit price | Amount |
|------|-------|------------|--------|
| Priced essays licensed (flat offers) | 1,340 licences | 3.00 – 9.99 EUR | 6,412.60 EUR |
| Metered essays (per character, per token, per access) | 27,900,000 units | 0.00002 – 0.03 EUR | 1,118.40 EUR |
| Free essays delivered under licence | 4,215 deliveries | 0.00 EUR | 0.00 EUR |
| **Illustrative gross revenue** | | | **7,531.00 EUR** |

Three things are worth noticing. The free essays are the largest count and
the smallest line: they are licensed all the same, so the publisher knows who
read them and under which terms, but they do not pay. The flat offers carry
most of the money because each one is a whole essay bought outright. The
metered line looks small per unit and adds up only because agents keep coming
back.

## How each line is calculated

A FORA offer states one term per resource. The Exchange signs the offer, the
agent accepts it, and the transaction log records the price the agent
authorised. The settled charge is that authorised amount. It is never
recomputed later from a quantity the agent reports, which is what makes the
table above reproducible from the log alone.

- A **flat** term pays the stated price once per licence. Nine essays at
  7.50 EUR is 67.50 EUR, whatever the agent does with the text afterwards.
- A **per-unit** term pays the rate multiplied by the metered quantity the
  offer named: characters, tokens or accesses. Where the term carries a daily
  quota, the count stops at the quota.
- A **free** term pays nothing and still produces a transaction record.

## What the publisher did not earn

Every prohibited use is a line that does not exist. Most Stoa Press terms
prohibit `ai-train`, so no training licence was sold and no training revenue
appears. A publisher that wants that line has to offer it.

This article is itself a licensed resource. Its FORA offer is a flat price of
0.05 EUR per licence, and that is the only number in this article that is
real for the demo: it is the price an agent pays to read this text.

---

*Stoa Press · Publisher Perspectives · Markdown edition · illustrative demo material*
