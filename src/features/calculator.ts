import { randomInt } from "node:crypto";
import Fraction from "fraction.js";

const bits = 10000;
const maxDice = 100;
const maxDiceSides = 2n ** 48n - 1n;
function checked(value: Fraction): Fraction {
  if (value.n.toString(2).length > bits || value.d.toString(2).length > bits) {
    throw new Error("计算结果过大");
  }
  return value;
}
function integer(value: Fraction): bigint {
  if (value.d !== 1n) throw new Error("此运算需要整数");
  return value.s * value.n;
}
function product(n: bigint, k: bigint, combination = false): Fraction {
  if (n < 0n || k < 0n || k > n) throw new Error("排列/组合范围无效");
  if (combination && k > n - k) k = n - k;
  if (k > 2000n) throw new Error("计算结果过大");
  let result = 1n;
  for (let i = 0n; i < k; i++) {
    result = result * (n - i) / (combination ? i + 1n : 1n);
    if (result.toString(2).length > bits) throw new Error("计算结果过大");
  }
  return new Fraction(result);
}
function power(base: Fraction, exponent: Fraction): Fraction {
  if (exponent.d === 1n) {
    const value = integer(exponent);
    if (base.n === 1n && base.d === 1n) return new Fraction(base.s === -1n && value % 2n ? -1 : 1);
    if (
      value > 10000n || value < -10000n ||
      Number(value < 0n ? -value : value) *
            Math.max(base.n.toString(2).length - 1, base.d.toString(2).length - 1) > bits
    ) throw new Error("计算结果过大");
    return checked(base.pow(exponent)!);
  }
  const result = Math.pow(base.valueOf(), exponent.valueOf());
  if (!Number.isFinite(result)) throw new Error("计算结果不是有限实数");
  return checked(new Fraction(String(result)));
}
function binary(op: string, a: Fraction, b: Fraction): Fraction {
  switch (op) {
    case "+":
      return a.add(b);
    case "-":
      return a.sub(b);
    case "*":
      return a.mul(b);
    case "/":
      return a.div(b);
    case "^":
    case "**":
      return power(a, b);
    case "//": {
      const value = a.div(b);
      return new Fraction(value.s * (value.n / value.d));
    }
    case "%": {
      const raw = integer(b), divisor = raw < 0n ? -raw : raw;
      if (!divisor) throw new Error("不能除以零");
      return new Fraction(((integer(a) % divisor) + divisor) % divisor);
    }
    case "p":
    case "a":
      return product(integer(a), integer(b));
    case "c":
      return product(integer(a), integer(b), true);
    default:
      throw new Error("无法识别运算符");
  }
}
const precedence: Record<string, number> = {
  "+": 1,
  "-": 1,
  "*": 2,
  "/": 2,
  "//": 2,
  "%": 2,
  "^": 4,
  "**": 4,
  p: 5,
  a: 5,
  c: 5,
  d: 6,
};

export function normalizeExpression(text: string): string {
  return text.normalize("NFKC").replaceAll("×", "*").replaceAll("÷", "/").replaceAll("【", "(")
    .replaceAll("】", ")");
}
export function isCalculation(text: string): boolean {
  const value = normalizeExpression(text);
  return value.length <= 1000 && /[+\-*/^!%√|pacd]/i.test(value) &&
    /^[\d\s.()+\-*/^!%√|epiacd]+$/i.test(value) && !/^\+\d+$/.test(value) &&
    (!/d/i.test(value) || /[\d)!|%]\s*d\s*[\d(]/i.test(value));
}

export function calculate(text: string): Fraction {
  return evaluateCalculation(text).value;
}

export function evaluateCalculation(
  text: string,
  sample: (exclusiveMaximum: number) => number = randomInt,
) {
  const source = normalizeExpression(text);
  if (source.length > 1000) throw new Error("表达式过长");
  const tokens: string[] = [];
  let rest = source.trim();
  while (rest) {
    const token = rest.match(/^(?:\d+(?:\.\d*)?|\.\d+|pi|e|\*\*|\/\/|[()+\-*/^!%√|pacd])/i)?.[0];
    if (!token) throw new Error("表达式包含无法识别的内容");
    tokens.push(token.toLowerCase());
    rest = rest.slice(token.length).trimStart();
  }
  const dice: { sides: bigint; rolls: number[] }[] = [];
  let diceRemaining = maxDice;
  let index = 0, depth = 0;
  const expect = (token: string) => {
    if (tokens[index++] !== token) throw new Error("括号不匹配");
  };
  const starts = (token: string | undefined) =>
    Boolean(token && /^(?:\d|\.|pi$|e$|\(|√|\+|-|\|)/.test(token));
  function expression(minimum = 0): Fraction {
    if (++depth > 128) throw new Error("表达式嵌套过深");
    const token = tokens[index++];
    let value: Fraction;
    if (token === "(") {
      value = expression();
      expect(")");
    } else if (token === "|") {
      value = expression().abs();
      expect("|");
    } else if (token === "+" || token === "-") {
      value = expression(3);
      if (token === "-") value = value.neg();
    } else if (token === "√") value = power(expression(3), new Fraction("0.5"));
    else if (token === "pi") value = new Fraction("3.141592653589793");
    else if (token === "e") value = new Fraction("2.718281828459");
    else if (token && /^(?:\d|\.)/.test(token)) value = new Fraction(token);
    else throw new Error("缺少数字");
    while (index < tokens.length) {
      const operator = tokens[index]!;
      if (operator === "!" && 5 > minimum) {
        index++;
        const n = integer(value);
        value = product(n, n);
        continue;
      }
      if (operator === "%" && !starts(tokens[index + 1]) && 5 > minimum) {
        index++;
        value = value.div(100);
        continue;
      }
      const priority = precedence[operator] ?? -1;
      if (priority <= minimum) break;
      index++;
      const right = expression(
        operator === "^" || operator === "**" ? priority - 1 : priority,
      );
      if (operator === "d") {
        const count = integer(value);
        const sides = integer(right);
        if (count < 1n || count > BigInt(diceRemaining) || sides < 1n || sides > maxDiceSides) {
          throw new Error("骰子数量或面数超出范围");
        }
        diceRemaining -= Number(count);
        const rolls = Array.from({ length: Number(count) }, () => sample(Number(sides)) + 1);
        dice.push({ sides, rolls });
        value = new Fraction(rolls.reduce((sum, roll) => sum + BigInt(roll), 0n));
      } else {
        value = checked(binary(operator, value, right));
      }
    }
    depth--;
    return checked(value);
  }
  const result = expression();
  if (index !== tokens.length) throw new Error("表达式不完整");
  return { value: result, dice };
}
function fixed(value: Fraction, places: number): string {
  const scale = 10n ** BigInt(places), scaled = value.n * scale;
  const rounded = scaled / value.d + (scaled % value.d * 2n >= value.d ? 1n : 0n);
  const whole = rounded / scale;
  const decimal = (rounded % scale).toString().padStart(places, "0").replace(/0+$/, "");
  return `${value.s === -1n && rounded ? "-" : ""}${whole}${decimal ? "." + decimal : ""}`;
}
export function formatCalculation(value: Fraction): string {
  if (value.n === 0n || value.abs().compare(new Fraction(1, 20000)) >= 0) return fixed(value, 4);
  let exponent = value.n.toString().length - value.d.toString().length;
  let factor = exponent >= 0
    ? new Fraction(10n ** BigInt(exponent))
    : new Fraction(1n, 10n ** BigInt(-exponent));
  if (value.abs().compare(factor) < 0) {
    exponent--;
    factor = factor.div(10);
  }
  let mantissa = fixed(value.div(factor), 3);
  if (mantissa === "10" || mantissa === "-10") {
    exponent++;
    mantissa = mantissa.startsWith("-") ? "-1" : "1";
  }
  return `${mantissa}e${exponent}`;
}
