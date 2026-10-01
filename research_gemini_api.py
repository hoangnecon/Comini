import json

# This is a conceptual script to show how we would trace the Gemini API response
# We will intercept the SSE stream from generativelanguage.googleapis.com

def parse_gemini_stream_chunk(chunk_str):
    """
    Parses a single SSE chunk from Gemini API:
    data: {"candidates": [{"content": {"parts": [{"text": "hello", "thought": true}]}}]}
    """
    try:
        if chunk_str.startswith("data: "):
            data = json.loads(chunk_str[6:])
            parts = data.get("candidates", [{}])[0].get("content", {}).get("parts", [])
            for part in parts:
                text = part.get("text", "")
                is_thought = part.get("thought", False)
                print(f"[Thought={is_thought}] {text}")
    except Exception as e:
        pass

# Example mock data based on our research
mock_stream = [
    'data: {"candidates": [{"content": {"parts": [{"text": "Let me think about this.\\n", "thought": true}]}}]}',
    'data: {"candidates": [{"content": {"parts": [{"text": "1 + 1 is 2.\\n", "thought": true}]}}]}',
    'data: {"candidates": [{"content": {"parts": [{"text": "The answer is 2."}]}}]}'
]

for chunk in mock_stream:
    parse_gemini_stream_chunk(chunk)
