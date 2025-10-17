#!/usr/bin/env python3
"""
Analyze Claude Code transcript files for token usage patterns.
Used for debugging cache invalidation detection and analyzing token usage per message.
"""

import json
import sys
import re
from pathlib import Path
from datetime import datetime
try:
    from urllib.request import urlopen
    from urllib.error import URLError
except ImportError:
    urlopen = None

def format_number(n):
    """Format large numbers with k/m suffix"""
    if n >= 1_000_000:
        return f"{n/1_000_000:.1f}m"
    elif n >= 1_000:
        return f"{n/1_000:.1f}k"
    return str(n)

# Pricing per million tokens (updated January 2025)
# Source: https://docs.claude.com/en/docs/about-claude/pricing
# Note: Uses 5-minute cache TTL pricing (Claude Code default)
PRICING = {
    # Sonnet 4.5
    'claude-sonnet-4-5-20250929': {
        'input': 3.00,
        'cache_write': 3.75,  # 5m TTL
        'cache_read': 0.30,
        'output': 15.00,
    },
    # Sonnet 3.5 (deprecated)
    'claude-3-5-sonnet-20241022': {
        'input': 3.00,
        'cache_write': 3.75,  # 5m TTL
        'cache_read': 0.30,
        'output': 15.00,
    },
    # Haiku 4.5
    'claude-3-5-haiku-20250110': {
        'input': 1.00,
        'cache_write': 1.25,  # 5m TTL
        'cache_read': 0.10,
        'output': 5.00,
    },
    # Haiku 3.5
    'claude-3-5-haiku-20241022': {
        'input': 0.80,
        'cache_write': 1.00,  # 5m TTL
        'cache_read': 0.08,
        'output': 4.00,
    },
    # Opus 4.1
    'claude-opus-4-1-20250514': {
        'input': 15.00,
        'cache_write': 18.75,  # 5m TTL
        'cache_read': 1.50,
        'output': 75.00,
    },
}

def analyze_transcript(transcript_path):
    """Analyze transcript file and show token usage per message"""

    if not Path(transcript_path).exists():
        print(f"Error: File not found: {transcript_path}")
        return

    print(f"Analyzing: {transcript_path}\n")

    # Print table header
    header = f"{'Msg#':>5} {'Input':>8} {'Output':>8} {'CacheR':>8} {'CacheC':>8} {'Ctx':>8} {'Eff%':>7} {'Event'}"
    print(header)
    print("=" * 130)

    message_num = 0
    prev_cache_read = 0
    prev_cache_create = 0
    total_input = 0
    total_output = 0
    total_cache_read = 0
    total_cache_create = 0

    # Track usage per model
    model_stats = {}

    with open(transcript_path, 'r') as f:
        for line_num, line in enumerate(f, 1):
            if not line.strip():
                continue

            try:
                data = json.loads(line)
            except json.JSONDecodeError:
                continue

            # Extract usage data
            usage = None
            if 'message' in data and 'usage' in data['message']:
                usage = data['message']['usage']
            elif 'usage' in data:
                usage = data['usage']

            if not usage:
                continue

            message_num += 1

            # Extract model
            model_name = data.get('message', {}).get('model', 'unknown')

            # Extract token counts
            input_tokens = usage.get('input_tokens', 0)
            output_tokens = usage.get('output_tokens', 0)
            cache_read = usage.get('cache_read_input_tokens', 0)
            cache_create = usage.get('cache_creation_input_tokens', 0)

            # Update totals
            total_input += input_tokens
            total_output += output_tokens
            total_cache_read += cache_read
            total_cache_create += cache_create

            # Track cumulative fresh input tokens (overview of session growth)
            cumulative_input = total_input

            # Track per-model stats
            if model_name not in model_stats:
                model_stats[model_name] = {
                    'messages': 0,
                    'input': 0,
                    'output': 0,
                    'cache_read': 0,
                    'cache_create': 0
                }
            model_stats[model_name]['messages'] += 1
            model_stats[model_name]['input'] += input_tokens
            model_stats[model_name]['output'] += output_tokens
            model_stats[model_name]['cache_read'] += cache_read
            model_stats[model_name]['cache_create'] += cache_create

            # Calculate efficiency
            eff_str = "-"
            if cache_read > 0:
                efficiency = (cache_read / (input_tokens + cache_read)) * 100
                eff_str = f"{efficiency:.2f}"

            # Detect cache events
            cache_event = ""
            if cache_create > 0 and prev_cache_create == 0:
                cache_event = "🆕 CACHE START"
            elif cache_read > 0 and prev_cache_read == 0:
                cache_event = "⚡ CACHE READ"
            elif prev_cache_read > 0 and cache_read < prev_cache_read:
                drop = prev_cache_read - cache_read
                if drop >= 10000:  # Significant drop
                    cache_event = f"🔄 INVALIDATION (↓{format_number(drop)})"
            elif cache_read > prev_cache_read and prev_cache_read > 0:
                increase = cache_read - prev_cache_read
                if increase >= 1000:  # Only show significant growth
                    cache_event = f"📈 GREW (+{format_number(increase)})"

            # Format single-line output
            print(f"{message_num:>5} {format_number(input_tokens):>8} {format_number(output_tokens):>8} "
                  f"{format_number(cache_read):>8} {format_number(cache_create):>8} "
                  f"{format_number(cumulative_input):>8} {eff_str:>7} {cache_event}")

            prev_cache_read = cache_read
            prev_cache_create = cache_create

    # Summary
    print("=" * 130)
    print("\nSUMMARY:")
    print(f"Total Messages: {message_num}")
    print(f"\nInput Tokens:")
    print(f"  Fresh (non-cached): {format_number(total_input)}")
    print(f"  From Cache:         {format_number(total_cache_read)}")
    total_actual_input = total_input + total_cache_read
    print(f"  TOTAL INPUT:        {format_number(total_actual_input)}")
    print(f"\nOutput Tokens:        {format_number(total_output)}")
    print(f"Cache Written:        {format_number(total_cache_create)}")
    print(f"\nNote: 'Ctx' column shows cumulative fresh input tokens (running total)")

    if total_cache_read > 0:
        overall_efficiency = (total_cache_read / total_actual_input) * 100
        print(f"\nCache Efficiency: {overall_efficiency:.2f}%")

    # Per-model breakdown and costs
    print("\n" + "=" * 130)
    print("\nPER-MODEL BREAKDOWN & COSTS:")
    total_cost = 0.0

    for model_name, stats in sorted(model_stats.items()):
        print(f"\n{model_name}:")
        print(f"  Messages: {stats['messages']}")
        print(f"  Input:    {format_number(stats['input'])}")
        print(f"  Output:   {format_number(stats['output'])}")
        print(f"  Cache R:  {format_number(stats['cache_read'])}")
        print(f"  Cache W:  {format_number(stats['cache_create'])}")

        # Calculate cost if pricing is known
        if model_name in PRICING:
            pricing = PRICING[model_name]
            cost = (
                (stats['input'] / 1_000_000) * pricing['input'] +
                (stats['output'] / 1_000_000) * pricing['output'] +
                (stats['cache_read'] / 1_000_000) * pricing['cache_read'] +
                (stats['cache_create'] / 1_000_000) * pricing['cache_write']
            )
            total_cost += cost
            print(f"  Cost:     ${cost:.4f}")
        else:
            print(f"  Cost:     Unknown (pricing not available)")

    print(f"\n{'TOTAL COST:':<20} ${total_cost:.4f}")
    print(f"{'=':<20} ${total_cost:.2f}")

    # Show cost comparison without cache
    if total_cache_read > 0:
        # Calculate what it would cost without cache (all input as fresh)
        cost_without_cache = 0.0
        for model_name, stats in model_stats.items():
            if model_name in PRICING:
                pricing = PRICING[model_name]
                total_input_for_model = stats['input'] + stats['cache_read']
                cost = (
                    (total_input_for_model / 1_000_000) * pricing['input'] +
                    (stats['output'] / 1_000_000) * pricing['output']
                )
                cost_without_cache += cost

        savings = cost_without_cache - total_cost
        print(f"\nCost without cache:  ${cost_without_cache:.2f}")
        print(f"Savings from cache:  ${savings:.2f} ({(savings/cost_without_cache*100):.1f}%)")

if __name__ == "__main__":
    if len(sys.argv) < 2:
        print("Usage: python analyze_transcript.py <transcript.jsonl>")
        sys.exit(1)

    analyze_transcript(sys.argv[1])
