import { assertEquals, assertRejects, assertThrows } from "@std/assert";
import {
  calculate,
  evaluateCalculation,
  formatCalculation,
  isCalculation,
} from "../../src/features/calculator.ts";
import {
  exchange,
  formatExchangeUpdate,
  ocr,
  parseExchange,
  UnsupportedCurrencyError,
} from "../../src/features/tools.ts";
import { parseConfig } from "../../src/config.ts";

Deno.test("calculator preserves rational precedence, Go operators and small number formatting", () => {
  const examples = {
    "1/3+1/6": "0.5",
    "2^3^2": "512",
    "-2^2": "-4",
    "10C3": "120",
    "5P3": "60",
    "6!": "720",
    "√9+|1-5|": "7",
    "50%": "0.5",
    "-7//3": "-2",
    "-7%3": "2",
    "7%-3": "1",
    "3.14 ×（2＋1）": "9.42",
    "1/300000": "3.333e-6",
    "1/1000000000000000000000000000000": "1e-30",
  };
  for (const [expression, expected] of Object.entries(examples)) {
    assertEquals(formatCalculation(calculate(expression)), expected, expression);
  }
  assertEquals(isCalculation("10C3"), true);
  assertEquals(isCalculation("+1"), false);
  assertEquals(isCalculation("hello=1+2"), false);
  for (
    const value of ["1/0", "(-1)^0.5", "3000!", "2^100000", "(".repeat(150) + "1", "Deno.exit()"]
  ) {
    assertThrows(() => calculate(value));
  }
});

Deno.test("calculator samples each die independently with inclusive bounds", () => {
  const single = evaluateCalculation("1d100", (maximum) => maximum - 1);
  assertEquals(single.dice, [{ sides: 100n, rolls: [100] }]);
  assertEquals(formatCalculation(single.value), "100");

  const offsets = [0, 99, 2];
  const maxima: number[] = [];
  const mixed = evaluateCalculation("2 * 2D100 + 1d6", (maximum) => {
    maxima.push(maximum);
    return offsets.shift()!;
  });
  assertEquals(maxima, [100, 100, 6]);
  assertEquals(mixed.dice, [{ sides: 100n, rolls: [1, 100] }, { sides: 6n, rolls: [3] }]);
  assertEquals(formatCalculation(mixed.value), "205");
  assertEquals(isCalculation("2D100"), true);
  assertEquals(isCalculation("100 AED"), false);
  for (
    const expression of [
      "0d100",
      "1d0",
      "1.5d6",
      "2d6.5",
      "101d6",
      "100d1+1d1",
      "1d281474976710656",
    ]
  ) {
    assertThrows(() => evaluateCalculation(expression));
  }
});

Deno.test("exchange validates aliases, serializes concurrent fetches and rejects missing rates", async () => {
  const request = parseExchange("1.5 usd to RMB")!;
  assertEquals(request, { amount: 1.5, from: "USD", to: "CNY" });
  assertEquals(parseExchange("2 NTD"), { amount: 2, from: "TWD", to: "CNY" });
  assertEquals(parseExchange("1.4.3 usd"), undefined);
  assertEquals(parseExchange("2 USDT to RMB"), { amount: 2, from: "USDT", to: "CNY" });
  assertEquals(parseExchange("3 TONCOIN USD"), { amount: 3, from: "TONCOIN", to: "USD" });
  assertEquals(parseExchange("1 1INCH USDT"), { amount: 1, from: "1INCH", to: "USDT" });
  assertEquals(parseExchange("2 123"), undefined);
  assertEquals(parseExchange("9".repeat(400) + " USD"), undefined);
  let calls = 0;
  const fetcher: typeof fetch = () => {
    calls++;
    return Promise.resolve(
      Response.json({ date: "2026-10-12", usd: { cny: 7, usdt: 1 } }),
    );
  };
  const results = await Promise.all([exchange(request, fetcher), exchange(request, fetcher)]);
  assertEquals(calls, 1);
  assertEquals(results[0]!.result, 10.5);
  assertEquals(results[0]!.updated, { precision: "day", date: "2026-10-12" });
  assertEquals((await exchange({ ...request, to: "USDT" }, fetcher)).result, 1.5);
  await assertRejects(() => exchange({ ...request, to: "XYZ" }, fetcher), UnsupportedCurrencyError);
});

Deno.test("an unsupported base at one source still permits a successful fallback", async () => {
  let calls = 0;
  const fetcher: typeof fetch = () => {
    calls++;
    return Promise.resolve(
      calls === 1
        ? new Response(null, { status: 404 })
        : Response.json({ date: "2026-10-23", doge: { cny: 1.2 } }),
    );
  };
  const result = await exchange({ amount: 2, from: "DOGE", to: "CNY" }, fetcher);
  assertEquals(result.result, 2.4);
  assertEquals(calls, 2);
});

Deno.test("exchange falls back to Cloudflare after a network failure without replacing the declared date", async () => {
  const urls: string[] = [];
  const fetcher: typeof fetch = (input) => {
    const url = String(input);
    urls.push(url);
    if (url.includes("cdn.jsdelivr.net")) return Promise.reject(new TypeError("network"));
    assertEquals(url, "https://latest.currency-api.pages.dev/v1/currencies/eth.min.json");
    return Promise.resolve(Response.json({ date: "2025-12-31", eth: { cny: 20000 } }));
  };
  const result = await exchange({ amount: 2, from: "ETH", to: "CNY" }, fetcher);
  assertEquals(result.result, 40000);
  assertEquals(result.updated, { precision: "day", date: "2025-12-31" });
  assertEquals(urls.length, 2);
});

Deno.test("exchange supplements missing currencies using legacy rates and their own update timestamp", async () => {
  const urls: string[] = [];
  const fetcher: typeof fetch = (input) => {
    const url = String(input);
    urls.push(url);
    return Promise.resolve(
      url.includes("open.er-api.com")
        ? Response.json({
          result: "success",
          rates: { FOK: 7.4 },
          time_last_update_unix: 1791849600,
        })
        : Response.json({ date: "2026-10-12", eur: { cny: 8 } }),
    );
  };
  const result = await exchange({ amount: 2, from: "EUR", to: "FOK" }, fetcher);
  assertEquals(result.result, 14.8);
  assertEquals(result.updated, { precision: "second", unix: 1791849600 });
  assertEquals(urls.length, 3);
  const cached = await exchange({ amount: 2, from: "EUR", to: "CNY" }, fetcher);
  assertEquals(cached.result, 16);
  assertEquals(cached.updated, { precision: "day", date: "2026-10-12" });
  assertEquals(urls.length, 3);
});

Deno.test("exchange rejects invalid dates, malformed rates and service errors instead of inventing update times", async () => {
  let calls = 0;
  const fetcher: typeof fetch = () => {
    calls++;
    return Promise.resolve(
      calls % 3 === 1
        ? Response.json({ date: "2026-02-30", xau: { cny: 100 } })
        : calls % 3 === 2
        ? Response.json({ date: "2026-10-13", xau: { cny: "100" } })
        : new Response("private upstream error", { status: 503 }),
    );
  };
  await assertRejects(() => exchange({ amount: 1, from: "XAU", to: "CNY" }, fetcher));
  await assertRejects(() => exchange({ amount: 1, from: "XAU", to: "CNY" }, fetcher));
  assertEquals(calls, 6);
});

Deno.test("exchange displays declared timestamps in UTC+8 and preserves day precision across year boundaries", async () => {
  assertEquals(
    formatExchangeUpdate({ precision: "second", unix: Date.parse("2026-10-12T23:57:31Z") / 1000 }),
    "汇率更新于：2026-10-13 07:57:31（UTC+8）",
  );
  assertEquals(
    formatExchangeUpdate({ precision: "day", date: "2025-12-31" }),
    "汇率更新于：2025-12-31 08:00:00 至 2026-01-01 08:00:00 前（UTC+8，来源仅精确到日）",
  );
  const result = await exchange({ amount: 2, from: "BTC", to: "BTC" }, () => {
    throw new Error("identity conversion must not fetch");
  });
  assertEquals(result.result, 2);
  assertEquals(result.updated, undefined);
  assertEquals(formatExchangeUpdate(result.updated), "");
});

Deno.test("OCR uses configured API version and binary input and retains all lines", async () => {
  const config = parseConfig(
    "database-path: unused\nocr:\n  endpoint: https://ocr.test\n  api-key: fake\n  api-ver: custom\n  features: read\n  language: zh\n",
  ).ocr;
  const fetcher: typeof fetch = (input, init) => {
    const url = new URL(String(input));
    assertEquals(url.searchParams.get("api-version"), "custom");
    assertEquals(url.searchParams.get("language"), "zh");
    assertEquals(Array.from(init!.body as Uint8Array), [1, 2]);
    return Promise.resolve(
      Response.json({
        readResult: {
          blocks: [{ lines: [{ text: "一" }, { text: "二" }] }, { lines: [{ text: "三" }] }],
        },
      }),
    );
  };
  assertEquals(await ocr(new Uint8Array([1, 2]), config, fetcher), "一\n二\n\n三");
  await assertRejects(() =>
    ocr(new Uint8Array(), config, () => Promise.resolve(new Response("secret", { status: 401 })))
  );
});
