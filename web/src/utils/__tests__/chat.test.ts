import {
  preprocessLaTeX,
  replaceThinkToSection,
  stripEmptyThinkBlocks,
} from '../chat';

describe('preprocessLaTeX', () => {
  it('converts block \\[ \\] to $$ $$', () => {
    expect(preprocessLaTeX('\\[ x + y \\]')).toBe('$$x + y$$');
  });

  it('converts inline \\( \\) to $ $', () => {
    expect(preprocessLaTeX('\\( a \\)')).toBe('$a$');
  });

  it('does not cut block math at \\right] (Closes #13134)', () => {
    const content =
      '\\[ C_{seq}(y|x) = \\frac{1}{|y|} \\sum_{t=1}^{|y|} \\right] \\]';
    const result = preprocessLaTeX(content);
    expect(result).toContain('\\right]');
    expect(result).toContain('\\frac{1}{|y|}');
    expect(result).toBe(
      '$$ C_{seq}(y|x) = \\frac{1}{|y|} \\sum_{t=1}^{|y|} \\right] $$',
    );
  });

  it('does not cut inline math at \\big) or nested parens', () => {
    const content = '\\( f(x) + \\big) \\)';
    const result = preprocessLaTeX(content);
    expect(result).toContain('\\big)');
    expect(result).toBe('$ f(x) + \\big) $');
  });

  it('handles multiple block equations', () => {
    const content = 'First \\[ a \\] then \\[ b \\right] c \\]';
    const result = preprocessLaTeX(content);
    expect(result).toBe('First $$a$$ then $$ b \\right] c $$');
  });
});

describe('stripEmptyThinkBlocks', () => {
  it('removes empty redacted_thinking blocks', () => {
    expect(stripEmptyThinkBlocks('<think></think>你好')).toBe('你好');
  });

  it('removes empty think blocks with mixed closing tags', () => {
    const input = '<think></' + 'think>答案';
    expect(stripEmptyThinkBlocks(input)).toBe('答案');
  });

  it('keeps non-empty think blocks', () => {
    const input = '<think>推理内容</think>答案';
    expect(stripEmptyThinkBlocks(input)).toBe(input);
  });
});

describe('replaceThinkToSection', () => {
  it('does not render empty think blocks', () => {
    expect(replaceThinkToSection('<think></think>你好')).toBe('你好');
  });

  it('renders non-empty think blocks as details', () => {
    expect(replaceThinkToSection('<think>分析中</think>你好')).toBe(
      '<details class="think"><summary>Thinking...</summary>分析中</details>你好',
    );
  });

  it('drops an empty think section instead of rendering a bare strip', () => {
    expect(replaceThinkToSection('<think></think>Here is the answer.')).toBe(
      'Here is the answer.',
    );
  });

  it('drops a whitespace-only think section', () => {
    expect(replaceThinkToSection('<think>  \n </think>answer')).toBe('answer');
  });

  it('keeps a non-empty think section as a details block', () => {
    expect(replaceThinkToSection('<think>some reasoning</think>answer')).toBe(
      '<details class="think"><summary>Thinking...</summary>some reasoning</details>answer',
    );
  });

  it('uses the provided summary for non-empty sections', () => {
    expect(
      replaceThinkToSection('<think>reasoning</think>', 'Deep thought'),
    ).toBe(
      '<details class="think"><summary>Deep thought</summary>reasoning</details>',
    );
  });

  it('leaves text without think markers unchanged', () => {
    expect(replaceThinkToSection('plain answer')).toBe('plain answer');
  });
});
