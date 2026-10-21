/**
 * systemOneTest.ts defines decision-test request and result records shared by the form, stream
 * runner, and answer view. Question identifiers associate returned answers with submitted
 * definitions, while usage validity is carried separately by the surrounding completion event
 * and run state.
 */

/**
 * SystemOneTestPayload combines a textual or structured state with keyed decision questions.
 */
export interface SystemOneTestPayload {
  state: string | object // textual or structured decision state
  /**
   * Questions are keyed by submitted identifiers, which the answer view uses to restore
   * definition order and descriptions.
   */
  questions: Record<
    string,
    {
      type: 'noul' | 'choice' | 'score' // decision answer category discriminator
      instructions: string // question instructions for upstream
      /**
       * Choice uses keyed descriptions, score uses ordered levels, and noul optionally names
       * true and false meanings.
       */
      criteria?: Record<string, string> | string[]
    }
  >
}

/**
 * SystemOneTestResult contains keyed decision answers and optional token accounting reported by
 * the server.
 */
export interface SystemOneTestResult {
  model: string // reported decision model identifier
  answers: Record<
    string,
    {
      type: 'noul' | 'choice' | 'score' // decision answer category discriminator
      noul?: number // probability of true answer
      choice?: string // selected choice option identifier
      score?: number // reported continuous score value
      confidence?: number // reported answer confidence fraction
      probabilities?: Record<string, number> // probabilities by option identifier
      /**
       * Optional response descriptions take precedence over submitted score criteria in the
       * answer view when a matching key exists.
       */
      legend?: unknown
    }
  > // decision answers by question identifier
  /**
   * Token counts are displayed only when the enclosing run also marks decisionUsageValid as
   * true.
   */
  usage?: {
    input_tokens: number // input token count
    output_tokens: number // output token count
  }
}
